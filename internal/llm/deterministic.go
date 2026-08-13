package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

const (
	FixtureOrderLookupMessage       = "Look up order AG-1042."
	FixtureReplacementMessage       = "Order AG-1042 arrived damaged. Replace it."
	FixtureReplacementCouponMessage = "My headphones arrived damaged. Can I get a replacement and a coupon?"
)

var ErrUnsupportedFixture = errors.New("fixture planner example unsupported")

// FixturePlanner is deterministic test/demo behavior for the documented synthetic examples.
type FixturePlanner struct{}

func NewFixturePlanner() *FixturePlanner {
	return &FixturePlanner{}
}

func (*FixturePlanner) ProviderLabel() string {
	return "deterministic fixture planner"
}

func (*FixturePlanner) Plan(ctx context.Context, request PlanRequest) (agent.ActionPlan, error) {
	if err := ctx.Err(); err != nil {
		return agent.ActionPlan{}, err
	}
	switch request.UserMessage {
	case FixtureOrderLookupMessage:
		if !hasFixtureContracts(request.ToolContracts, "get_order") {
			return agent.ActionPlan{}, ErrUnsupportedFixture
		}
		return fixtureOrderLookupPlan(), nil
	case FixtureReplacementMessage:
		if !hasFixtureContracts(request.ToolContracts, "get_order", "check_eligibility", "check_inventory", "create_replacement") {
			return agent.ActionPlan{}, ErrUnsupportedFixture
		}
		damaged, ok := fixtureCitation(request.Evidence, "damaged_goods")
		if !ok {
			return agent.ActionPlan{}, ErrUnsupportedFixture
		}
		return fixtureReplacementPlan([]string{damaged}, false), nil
	case FixtureReplacementCouponMessage:
		if !hasFixtureContracts(request.ToolContracts, "get_order", "check_eligibility", "check_inventory", "create_replacement", "issue_coupon") {
			return agent.ActionPlan{}, ErrUnsupportedFixture
		}
		damaged, hasDamaged := fixtureCitation(request.Evidence, "damaged_goods")
		coupon, hasCoupon := fixtureCitation(request.Evidence, "customer_care")
		if !hasDamaged || !hasCoupon {
			return agent.ActionPlan{}, ErrUnsupportedFixture
		}
		return fixtureReplacementPlan([]string{damaged, coupon}, true), nil
	default:
		return agent.ActionPlan{}, ErrUnsupportedFixture
	}
}

func (planner *FixturePlanner) Repair(ctx context.Context, request RepairRequest) (agent.ActionPlan, error) {
	return planner.Plan(ctx, request.Original)
}

func fixtureOrderLookupPlan() agent.ActionPlan {
	return agent.ActionPlan{
		Goal: "look up order", PolicyRefs: []string{},
		Steps: []agent.Step{{
			ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`),
			DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "order.exists",
		}},
	}
}

func fixtureReplacementPlan(policyRefs []string, includeCoupon bool) agent.ActionPlan {
	plan := agent.ActionPlan{
		Goal: "replace damaged item", PolicyRefs: append([]string{}, policyRefs...),
		Steps: []agent.Step{
			{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "order.exists"},
			{ID: "eligibility", Tool: "check_eligibility", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read, SuccessCondition: "eligibility.eligible"},
			{ID: "inventory", Tool: "check_inventory", Arguments: json.RawMessage(`{"sku":"HP-71"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "inventory.available"},
			{ID: "replace", Tool: "create_replacement", Arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"HP-71","reason":"damaged item"}`), DependsOn: []string{"eligibility", "inventory"}, Risk: toolkit.Write, SuccessCondition: "replacement.created"},
		},
	}
	if includeCoupon {
		plan.Goal = "replace damaged item and issue compensation"
		plan.Steps = append(plan.Steps, agent.Step{
			ID: "coupon", Tool: "issue_coupon", Arguments: json.RawMessage(`{"amount_cents":1500,"reason":"damaged item service recovery"}`),
			DependsOn: []string{"replace"}, Risk: toolkit.HighRiskWrite, SuccessCondition: "coupon.created", ApprovalRequired: true,
		})
	}
	return plan
}

func fixtureCitation(evidence []policy.Evidence, policyID string) (string, bool) {
	for _, item := range evidence {
		if item.PolicyID == policyID && item.CitationID != "" {
			return item.CitationID, true
		}
	}
	return "", false
}

func hasFixtureContracts(contracts []toolkit.Contract, names ...string) bool {
	required := make(map[string]bool, len(names))
	for _, name := range names {
		required[name] = false
	}
	for _, contract := range contracts {
		if _, exists := required[contract.Name]; exists && matchesCanonicalContract(contract) {
			required[contract.Name] = true
		}
	}
	for _, present := range required {
		if !present {
			return false
		}
	}
	return true
}

func matchesCanonicalContract(contract toolkit.Contract) bool {
	canonical, ok := toolkit.LookupContract(contract.Name)
	if !ok || contract.Name != canonical.Name || contract.Description != canonical.Description ||
		contract.Risk != canonical.Risk || contract.Scope != canonical.Scope ||
		contract.RequiresIdempotencyKey != canonical.RequiresIdempotencyKey {
		return false
	}
	return schemasEquivalent(contract.InputSchema, canonical.InputSchema)
}

func schemasEquivalent(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
