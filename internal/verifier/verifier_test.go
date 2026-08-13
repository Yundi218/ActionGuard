package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

var verifierTestNow = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

type factCall struct {
	userID  string
	orderID string
}

type fakeFactReader struct {
	orders map[string]commerce.Order
	err    error
	calls  []factCall
}

func (reader *fakeFactReader) GetOrder(_ context.Context, userID, orderID string) (commerce.Order, error) {
	reader.calls = append(reader.calls, factCall{userID: userID, orderID: orderID})
	if reader.err != nil {
		return commerce.Order{}, reader.err
	}
	order, ok := reader.orders[orderID]
	if !ok {
		return commerce.Order{}, commerce.ErrNotFound
	}
	return order, nil
}

func TestVerifyAndSealAcceptsReplacementAndBindsPrivateContext(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	verificationContext.Scopes = []string{
		"replacement:write", "order:read", "inventory:read", "eligibility:read", "order:read",
	}

	sealed, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	if !result.Valid || len(result.Errors) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if sealed.UserID() != verificationContext.UserID {
		t.Fatalf("UserID() = %q, want %q", sealed.UserID(), verificationContext.UserID)
	}
	wantScopes := []string{"eligibility:read", "inventory:read", "order:read", "replacement:write"}
	if got := sealed.Scopes(); !reflect.DeepEqual(got, wantScopes) {
		t.Fatalf("Scopes() = %#v, want %#v", got, wantScopes)
	}
	wantDigest := sha256.Sum256([]byte(strings.Join(wantScopes, "\x00")))
	if got := sealed.ScopeDigest(); got != wantDigest {
		t.Fatalf("ScopeDigest() = %x, want %x", got, wantDigest)
	}
	if len(reader.calls) != 1 || reader.calls[0] != (factCall{userID: verificationContext.UserID, orderID: "AG-1042"}) {
		t.Fatalf("fact calls = %#v, want one trusted-user lookup", reader.calls)
	}

	scopes := sealed.Scopes()
	scopes[0] = "attacker:write"
	returnedPlan := sealed.Plan()
	returnedPlan.Goal = "mutated"
	returnedPlan.PolicyRefs[0] = "mutated"
	returnedPlan.Steps[0].Tool = "issue_refund"
	returnedPlan.Steps[0].Arguments[0] = '['
	returnedPlan.Steps[1].DependsOn[0] = "mutated"
	plan.Steps[0].Tool = "issue_coupon"
	if got := sealed.Scopes(); !reflect.DeepEqual(got, wantScopes) {
		t.Fatalf("sealed scopes mutated through accessor: %#v", got)
	}
	gotPlan := sealed.Plan()
	if gotPlan.Goal != "replace damaged item" || gotPlan.PolicyRefs[0] != replacementCitationID || gotPlan.Steps[0].Tool != "get_order" || gotPlan.Steps[0].Arguments[0] != '{' || gotPlan.Steps[1].DependsOn[0] != "order" {
		t.Fatalf("sealed plan mutated through caller data: %#v", gotPlan)
	}
}

func TestVerifierRejectsInvalidStructureAndContracts(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*agent.ActionPlan)
	}{
		{name: "unknown dependency", code: "unknown_dependency", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[3].DependsOn = append(plan.Steps[3].DependsOn, "missing")
		}},
		{name: "missing required prerequisite", code: "missing_prerequisite", mutate: func(plan *agent.ActionPlan) {
			plan.Steps = append(plan.Steps[:2], plan.Steps[3])
			plan.Steps[2].DependsOn = []string{"eligibility"}
		}},
		{name: "self dependency", code: "self_dependency", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[0].DependsOn = []string{"order"}
		}},
		{name: "cycle", code: "dependency_cycle", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[0].DependsOn = []string{"eligibility"}
		}},
		{name: "duplicate step id", code: "duplicate_step_id", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[1].ID = "order"
		}},
		{name: "outside terminal prerequisite graph", code: "prerequisite_not_allowed", mutate: func(plan *agent.ActionPlan) {
			shipment := agent.Step{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read, SuccessCondition: "shipment.exists"}
			plan.Steps = append(plan.Steps, shipment)
			plan.Steps[3].DependsOn = append(plan.Steps[3].DependsOn, "shipment")
		}},
		{name: "duplicate tool and arguments", code: "duplicate_action", mutate: func(plan *agent.ActionPlan) {
			duplicate := cloneStep(plan.Steps[2])
			duplicate.ID = "inventory-again"
			plan.Steps = append(plan.Steps, duplicate)
			plan.Steps[3].DependsOn = append(plan.Steps[3].DependsOn, duplicate.ID)
		}},
		{name: "multiple terminals", code: "terminal_count", mutate: func(plan *agent.ActionPlan) {
			plan.Steps = append(plan.Steps, agent.Step{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "shipment.exists"})
		}},
		{name: "unknown tool", code: "unknown_tool", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[2].Tool = "delete_order"
		}},
		{name: "invalid tool arguments", code: "invalid_arguments", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[3].Arguments = json.RawMessage(`{"order_id":"AG-1042","sku":"SKU-RED-42"}`)
		}},
		{name: "risk mismatch", code: "risk_mismatch", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[3].Risk = toolkit.Read
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, verificationContext, reader := validReplacementFixture()
			tt.mutate(&plan)
			sealed, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
			requireErrorCode(t, result, tt.code)
			if sealed.UserID() != "" || len(sealed.Plan().Steps) != 0 {
				t.Fatalf("invalid plan was sealed: %#v", sealed)
			}
		})
	}
}

func TestVerifierReturnsStableRepairErrorForMissingScope(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	verificationContext.Scopes = []string{"order:read", "eligibility:read", "inventory:read"}

	_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	want := Result{Errors: []Error{{
		Code: "missing_scope", StepID: "replace", Field: "scope", Message: "required tool scope is missing",
	}}}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	encoded, err := json.Marshal(result.Errors[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, wantJSON := string(encoded), `{"code":"missing_scope","step_id":"replace","field":"scope","message":"required tool scope is missing"}`; got != wantJSON {
		t.Fatalf("error JSON = %s, want %s", got, wantJSON)
	}
}

func TestVerifierRejectsCrossUserOrderAndUnavailableFacts(t *testing.T) {
	tests := []struct {
		name   string
		reader *fakeFactReader
		code   string
	}{
		{name: "cross user", reader: &fakeFactReader{orders: map[string]commerce.Order{"AG-1042": {ID: "AG-1042", UserID: "user-other", PaidAmountCents: 12000}}}, code: "order_not_owned"},
		{name: "fact error", reader: &fakeFactReader{err: errors.New("database-private failure")}, code: "fact_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, verificationContext, _ := validReplacementFixture()
			_, result := New(tt.reader).VerifyAndSeal(context.Background(), plan, verificationContext)
			requireErrorCode(t, result, tt.code)
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "database-private") || strings.Contains(string(encoded), "user-other") {
				t.Fatalf("verification error leaked private facts: %s", encoded)
			}
		})
	}
}

func TestVerifierRequiresExactApplicableTypedEvidence(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*agent.ActionPlan, *Context)
	}{
		{name: "missing citation", code: "missing_citation", mutate: func(plan *agent.ActionPlan, _ *Context) {
			plan.PolicyRefs = nil
		}},
		{name: "non exact citation", code: "citation_not_found", mutate: func(plan *agent.ActionPlan, _ *Context) {
			plan.PolicyRefs[0] += ":partial"
		}},
		{name: "expired", code: "evidence_not_applicable", mutate: func(_ *agent.ActionPlan, verificationContext *Context) {
			verificationContext.Evidence[0].EffectiveTo = verifierTestNow
		}},
		{name: "wrong region", code: "evidence_not_applicable", mutate: func(_ *agent.ActionPlan, verificationContext *Context) {
			verificationContext.Evidence[0].Region = "US"
		}},
		{name: "wrong category", code: "evidence_not_applicable", mutate: func(_ *agent.ActionPlan, verificationContext *Context) {
			verificationContext.Evidence[0].ProductCategory = "apparel"
		}},
		{name: "wrong typed risk", code: "evidence_not_applicable", mutate: func(_ *agent.ActionPlan, verificationContext *Context) {
			verificationContext.Evidence[0].RiskLevel = toolkit.Read
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, verificationContext, reader := validReplacementFixture()
			tt.mutate(&plan, &verificationContext)
			_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
			requireErrorCode(t, result, tt.code)
		})
	}
}

func TestVerifierRejectsRefundAboveCurrentBalance(t *testing.T) {
	plan := refundPlan(9001, true, "refund.created")
	verificationContext := Context{
		UserID: "user-018", Scopes: []string{"order:read", "refund:write"}, Now: verifierTestNow,
		Evidence: []policy.Evidence{refundEvidence()},
	}
	reader := &fakeFactReader{orders: map[string]commerce.Order{
		"AG-1042": {ID: "AG-1042", UserID: "user-018", PaidAmountCents: 12000, RefundedAmountCents: 3000},
	}}
	_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	requireErrorCode(t, result, "refund_amount_exceeds_balance")
}

func TestVerifierRejectsCouponAboveTypedEvidenceCap(t *testing.T) {
	capCents := int64(2000)
	evidence := baseEvidence("customer_care", "v1", "Coupon compensation", toolkit.HighRiskWrite)
	evidence.MaxCouponCents = &capCents
	plan := agent.ActionPlan{
		Goal: "issue service recovery coupon", PolicyRefs: []string{evidence.CitationID},
		Steps: []agent.Step{{ID: "coupon", Tool: "issue_coupon", Arguments: json.RawMessage(`{"amount_cents":2001,"reason":"service recovery"}`), DependsOn: []string{}, Risk: toolkit.HighRiskWrite, SuccessCondition: "coupon.created", ApprovalRequired: true}},
	}
	verificationContext := Context{UserID: "user-018", Scopes: []string{"coupon:write"}, Now: verifierTestNow, Evidence: []policy.Evidence{evidence}}
	_, result := New(nil).VerifyAndSeal(context.Background(), plan, verificationContext)
	requireErrorCode(t, result, "coupon_amount_exceeds_cap")
}

func TestVerifierRequiresWritePostconditionAndHighRiskApproval(t *testing.T) {
	t.Run("write postcondition", func(t *testing.T) {
		plan, verificationContext, reader := validReplacementFixture()
		plan.Steps[3].SuccessCondition = ""
		_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
		requireErrorCode(t, result, "invalid_postcondition")
	})

	t.Run("high risk approval", func(t *testing.T) {
		plan := refundPlan(500, false, "refund.created")
		verificationContext := Context{UserID: "user-018", Scopes: []string{"order:read", "refund:write"}, Now: verifierTestNow, Evidence: []policy.Evidence{refundEvidence()}}
		reader := &fakeFactReader{orders: map[string]commerce.Order{"AG-1042": {ID: "AG-1042", UserID: "user-018", PaidAmountCents: 12000}}}
		_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
		requireErrorCode(t, result, "approval_required")
	})
}

const replacementCitationID = "damaged_goods:v3:Replacement eligibility:10-120"

func validReplacementFixture() (agent.ActionPlan, Context, *fakeFactReader) {
	plan := agent.ActionPlan{
		Goal:       "replace damaged item",
		PolicyRefs: []string{replacementCitationID},
		Steps: []agent.Step{
			{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "order.exists"},
			{ID: "eligibility", Tool: "check_eligibility", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read, SuccessCondition: "eligibility.eligible"},
			{ID: "inventory", Tool: "check_inventory", Arguments: json.RawMessage(`{"sku":"SKU-RED-42"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "inventory.available"},
			{ID: "replace", Tool: "create_replacement", Arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"SKU-RED-42","reason":"damaged"}`), DependsOn: []string{"eligibility", "inventory"}, Risk: toolkit.Write, SuccessCondition: "replacement.created"},
		},
	}
	verificationContext := Context{
		UserID: "user-018",
		Scopes: []string{"order:read", "eligibility:read", "inventory:read", "replacement:write"},
		Now:    verifierTestNow,
		Evidence: []policy.Evidence{
			baseEvidence("damaged_goods", "v3", "Replacement eligibility", toolkit.Write),
		},
	}
	reader := &fakeFactReader{orders: map[string]commerce.Order{
		"AG-1042": {ID: "AG-1042", UserID: "user-018", SKU: "SKU-RED-42", PaidAmountCents: 12000},
	}}
	return plan, verificationContext, reader
}

func baseEvidence(policyID, version, section string, risk toolkit.Risk) policy.Evidence {
	evidence := policy.Evidence{
		PolicyID: policyID, Version: version, Section: section, StartOffset: 10, EndOffset: 120,
		EffectiveFrom: verifierTestNow.Add(-24 * time.Hour), EffectiveTo: verifierTestNow.Add(24 * time.Hour),
		Region: "CN", ProductCategory: "electronics", RiskLevel: risk, Text: "synthetic policy evidence",
	}
	evidence.CitationID = policyID + ":" + version + ":" + section + ":10-120"
	return evidence
}

func refundEvidence() policy.Evidence {
	return baseEvidence("refunds", "v2", "Refundable balance", toolkit.HighRiskWrite)
}

func refundPlan(amount int64, approval bool, successCondition string) agent.ActionPlan {
	evidence := refundEvidence()
	return agent.ActionPlan{
		Goal: "refund order", PolicyRefs: []string{evidence.CitationID},
		Steps: []agent.Step{
			{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "order.exists"},
			{ID: "refund", Tool: "issue_refund", Arguments: json.RawMessage(`{"order_id":"AG-1042","amount_cents":` + jsonInt(amount) + `}`), DependsOn: []string{"order"}, Risk: toolkit.HighRiskWrite, SuccessCondition: successCondition, ApprovalRequired: approval},
		},
	}
}

func jsonInt(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func cloneStep(step agent.Step) agent.Step {
	step.Arguments = append(json.RawMessage(nil), step.Arguments...)
	step.DependsOn = append([]string{}, step.DependsOn...)
	return step
}

func requireErrorCode(t *testing.T, result Result, code string) {
	t.Helper()
	if result.Valid {
		t.Fatalf("result.Valid = true, want error %q", code)
	}
	for _, verificationError := range result.Errors {
		if verificationError.Code == code {
			return
		}
	}
	t.Fatalf("errors = %#v, want code %q", result.Errors, code)
}
