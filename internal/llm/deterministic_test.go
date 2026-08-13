package llm

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/Yundi218/ActionGuard/internal/verifier"
)

func TestFixturePlannerSupportsOnlyDocumentedSyntheticExamples(t *testing.T) {
	capCents := int64(2000)
	evidence := []policy.Evidence{
		{CitationID: "damaged_goods:v3:Replacement eligibility:10-120", PolicyID: "damaged_goods", Version: "v3", Text: "synthetic replacement evidence"},
		{CitationID: "customer_care:v1:Coupon compensation:15-210", PolicyID: "customer_care", Version: "v1", MaxCouponCents: &capCents, Text: "synthetic coupon evidence"},
	}
	planner := NewFixturePlanner()
	tests := []struct {
		name         string
		message      string
		evidence     []policy.Evidence
		wantTools    []string
		wantRefs     []string
		wantApproval bool
	}{
		{name: "order lookup", message: FixtureOrderLookupMessage, wantTools: []string{"get_order"}, wantRefs: []string{}},
		{name: "damaged item replacement", message: FixtureReplacementMessage, evidence: evidence, wantTools: []string{"get_order", "check_eligibility", "check_inventory", "create_replacement"}, wantRefs: []string{evidence[0].CitationID}},
		{name: "replacement plus coupon waits for runtime", message: FixtureReplacementCouponMessage, evidence: evidence, wantTools: []string{"get_order", "check_eligibility", "check_inventory", "create_replacement", "issue_coupon"}, wantRefs: []string{evidence[0].CitationID, evidence[1].CitationID}, wantApproval: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.Plan(context.Background(), PlanRequest{
				UserMessage: test.message, Evidence: test.evidence, ToolContracts: toolkit.Registry(),
			})
			if err != nil {
				t.Fatal(err)
			}
			var tools []string
			for _, step := range plan.Steps {
				tools = append(tools, step.Tool)
			}
			if !reflect.DeepEqual(tools, test.wantTools) || !reflect.DeepEqual(plan.PolicyRefs, test.wantRefs) {
				t.Fatalf("tools/refs = %v/%v, want %v/%v", tools, plan.PolicyRefs, test.wantTools, test.wantRefs)
			}
			if plan.Steps[len(plan.Steps)-1].ApprovalRequired != test.wantApproval {
				t.Fatalf("terminal approval_required = %v, want %v", plan.Steps[len(plan.Steps)-1].ApprovalRequired, test.wantApproval)
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := agent.DecodeActionPlan(encoded); err != nil {
				t.Fatalf("fixture plan violates strict ActionPlan contract: %v", err)
			}
		})
	}
}

func TestFixturePlannerRejectsUndocumentedOrIncompleteExamples(t *testing.T) {
	planner := NewFixturePlanner()
	tests := []PlanRequest{
		{UserMessage: "Please do anything useful for AG-1042", ToolContracts: toolkit.Registry()},
		{UserMessage: "  " + FixtureOrderLookupMessage + "  ", ToolContracts: toolkit.Registry()},
		{UserMessage: FixtureReplacementMessage, ToolContracts: toolkit.Registry()},
		{UserMessage: FixtureReplacementCouponMessage, Evidence: []policy.Evidence{{CitationID: "damaged", PolicyID: "damaged_goods"}}, ToolContracts: toolkit.Registry()},
		{UserMessage: FixtureOrderLookupMessage, ToolContracts: []toolkit.Contract{}},
	}
	for _, request := range tests {
		if _, err := planner.Plan(context.Background(), request); !errors.Is(err, ErrUnsupportedFixture) {
			t.Fatalf("request %q error = %v, want ErrUnsupportedFixture", request.UserMessage, err)
		}
	}
}

func TestFixturePlannerRequiresOnlyContractsUsedByTheExample(t *testing.T) {
	getOrder, ok := toolkit.LookupContract("get_order")
	if !ok {
		t.Fatal("get_order contract missing")
	}
	plan, err := NewFixturePlanner().Plan(context.Background(), PlanRequest{
		UserMessage: FixtureOrderLookupMessage, ToolContracts: []toolkit.Contract{getOrder},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Tool != "get_order" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestFixturePlannerRejectsMismatchedCanonicalToolContracts(t *testing.T) {
	canonical, ok := toolkit.LookupContract("get_order")
	if !ok {
		t.Fatal("get_order contract missing")
	}
	tests := []struct {
		name   string
		mutate func(*toolkit.Contract)
	}{
		{name: "scope", mutate: func(contract *toolkit.Contract) { contract.Scope = "shipment:read" }},
		{name: "risk", mutate: func(contract *toolkit.Contract) { contract.Risk = toolkit.Write }},
		{name: "idempotency requirement", mutate: func(contract *toolkit.Contract) { contract.RequiresIdempotencyKey = true }},
		{name: "input schema", mutate: func(contract *toolkit.Contract) {
			contract.InputSchema = json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"integer"}},"required":["order_id"],"type":"object"}`)
		}},
		{name: "duplicate nested schema key", mutate: func(contract *toolkit.Contract) {
			contract.InputSchema = json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"integer","description":"Order identifier","type":"string"}},"required":["order_id"],"type":"object"}`)
		}},
		{name: "description", mutate: func(contract *toolkit.Contract) { contract.Description = "forged description" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forged := canonical
			forged.InputSchema = append(json.RawMessage(nil), canonical.InputSchema...)
			test.mutate(&forged)
			_, err := NewFixturePlanner().Plan(context.Background(), PlanRequest{
				UserMessage:   FixtureOrderLookupMessage,
				ToolContracts: []toolkit.Contract{forged},
			})
			if !errors.Is(err, ErrUnsupportedFixture) {
				t.Fatalf("error = %v, want ErrUnsupportedFixture", err)
			}
		})
	}
}

func TestFixturePlannerAcceptsSemanticallyEquivalentToolSchemaJSON(t *testing.T) {
	getOrder, ok := toolkit.LookupContract("get_order")
	if !ok {
		t.Fatal("get_order contract missing")
	}
	getOrder.InputSchema = json.RawMessage(`{
		"type": "object",
		"required": ["order_id"],
		"properties": {
			"order_id": {
				"type": "string",
				"description": "Order identifier"
			}
		},
		"additionalProperties": false
	}`)
	plan, err := NewFixturePlanner().Plan(context.Background(), PlanRequest{
		UserMessage: FixtureOrderLookupMessage, ToolContracts: []toolkit.Contract{getOrder},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 1 || plan.Steps[0].Tool != "get_order" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestFixturePlannerRepairReplansDocumentedFixtureFromStructuredErrors(t *testing.T) {
	planner := NewFixturePlanner()
	request := PlanRequest{UserMessage: FixtureOrderLookupMessage, ToolContracts: toolkit.Registry()}
	prior := agent.ActionPlan{Goal: "bad plan"}
	errorsFromVerifier := []verifier.Error{{Code: "terminal_count", Field: "steps", Message: "plan must have exactly one terminal step"}}
	got, err := planner.Repair(context.Background(), RepairRequest{Original: request, Plan: prior, Errors: errorsFromVerifier})
	if err != nil {
		t.Fatal(err)
	}
	want, err := planner.Plan(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repair plan = %#v, want %#v", got, want)
	}
}

func TestFixturePlannerPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFixturePlanner().Plan(ctx, PlanRequest{UserMessage: FixtureOrderLookupMessage, ToolContracts: toolkit.Registry()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFixturePlannerIsLabeledFixtureBehavior(t *testing.T) {
	var _ Planner = NewFixturePlanner()
	if got := NewFixturePlanner().ProviderLabel(); got != "deterministic fixture planner" {
		t.Fatalf("provider label = %q", got)
	}
	if _, ok := any(NewFixturePlanner()).(*FixturePlanner); !ok {
		t.Fatalf("planner type = %T, want *FixturePlanner", NewFixturePlanner())
	}
}

func fixtureEvidence(policyID, citationID string) policy.Evidence {
	return policy.Evidence{
		PolicyID: policyID, CitationID: citationID,
		EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		EffectiveTo:   time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}
