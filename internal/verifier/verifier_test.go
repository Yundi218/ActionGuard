package verifier

import (
	"bytes"
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

type mutatingFactReader struct {
	plan  *agent.ActionPlan
	order commerce.Order
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

func (reader *mutatingFactReader) GetOrder(_ context.Context, _, _ string) (commerce.Order, error) {
	reader.plan.PolicyRefs[0] = "attacker:policy"
	reader.plan.Steps[0].Arguments[2] = 'X'
	reader.plan.Steps[1].DependsOn[0] = "attacker-step"
	reader.plan.Steps[4].Tool = "issue_coupon"
	return reader.order, nil
}

func TestVerifyAndSealAcceptsReplacementAndBindsPrivateContext(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	verificationContext.Scopes = []string{
		"replacement:write", "order:read", "shipment:read", "inventory:read", "eligibility:read", "order:read",
	}

	sealed, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	if !result.Valid || len(result.Errors) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if sealed.UserID() != verificationContext.UserID {
		t.Fatalf("UserID() = %q, want %q", sealed.UserID(), verificationContext.UserID)
	}
	wantScopes := []string{"eligibility:read", "inventory:read", "order:read", "replacement:write", "shipment:read"}
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

func TestVerifierRequiresShipmentBeforeReplacement(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	if !result.Valid {
		t.Fatalf("shipment-aware replacement result = %#v, want valid", result)
	}

	plan.Steps = append(plan.Steps[:1], plan.Steps[2:]...)
	plan.Steps[3].DependsOn = []string{"eligibility", "inventory"}
	_, result = New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	requireErrorCode(t, result, "missing_prerequisite")
}

func TestVerifyAndSealSnapshotsPlanBeforeFactCallbacks(t *testing.T) {
	plan, verificationContext, _ := validReplacementFixture()
	want := clonePlan(plan)
	reader := &mutatingFactReader{
		plan:  &plan,
		order: commerce.Order{ID: "AG-1042", UserID: "user-018", SKU: "SKU-RED-42", PaidAmountCents: 12000},
	}

	sealed, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	if !result.Valid || len(result.Errors) != 0 {
		t.Fatalf("result = %#v, want valid", result)
	}
	if got := sealed.Plan(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sealed plan = %#v, want pre-callback snapshot %#v", got, want)
	}
}

func TestVerifierRejectsInvalidStructureAndContracts(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*agent.ActionPlan)
	}{
		{name: "unknown dependency", code: "unknown_dependency", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[4].DependsOn = append(plan.Steps[4].DependsOn, "missing")
		}},
		{name: "missing required prerequisite", code: "missing_prerequisite", mutate: func(plan *agent.ActionPlan) {
			plan.Steps = append([]agent.Step{plan.Steps[0]}, plan.Steps[2:]...)
			plan.Steps[3].DependsOn = []string{"eligibility", "inventory"}
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
			refund := agent.Step{ID: "refund", Tool: "issue_refund", Arguments: json.RawMessage(`{"order_id":"AG-1042","amount_cents":100}`), DependsOn: []string{"order"}, Risk: toolkit.HighRiskWrite, SuccessCondition: "refund.created", ApprovalRequired: true}
			plan.Steps = append(plan.Steps, refund)
			plan.Steps[4].DependsOn = append(plan.Steps[4].DependsOn, "refund")
		}},
		{name: "duplicate tool and arguments", code: "duplicate_action", mutate: func(plan *agent.ActionPlan) {
			duplicate := cloneStep(plan.Steps[3])
			duplicate.ID = "inventory-again"
			plan.Steps = append(plan.Steps, duplicate)
			plan.Steps[4].DependsOn = append(plan.Steps[4].DependsOn, duplicate.ID)
		}},
		{name: "multiple terminals", code: "terminal_count", mutate: func(plan *agent.ActionPlan) {
			plan.Steps = append(plan.Steps, agent.Step{ID: "shipment-other", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1043"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "shipment.exists"})
		}},
		{name: "unknown tool", code: "unknown_tool", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[3].Tool = "delete_order"
		}},
		{name: "invalid tool arguments", code: "invalid_arguments", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[4].Arguments = json.RawMessage(`{"order_id":"AG-1042","sku":"SKU-RED-42"}`)
		}},
		{name: "risk mismatch", code: "risk_mismatch", mutate: func(plan *agent.ActionPlan) {
			plan.Steps[4].Risk = toolkit.Read
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

func TestVerifierBindsReplacementInventorySKU(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	plan.Steps[3].Arguments = json.RawMessage(`{"sku":"SKU-BLUE-99"}`)

	_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	requireErrorCode(t, result, "sku_mismatch")
}

func TestVerifierBindsEveryOrderStepToTrustedResolvedOrder(t *testing.T) {
	t.Run("read mismatch", func(t *testing.T) {
		plan, verificationContext, reader := singleToolFixture("get_order", json.RawMessage(`{"order_id":"AG-1043"}`))
		verificationContext.ResolvedOrderID = "AG-1042"
		reader.orders["AG-1043"] = commerce.Order{ID: "AG-1043", UserID: verificationContext.UserID, SKU: "SKU-RED-42"}

		sealed, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
		requireErrorCode(t, result, "resolved_order_mismatch")
		if sealed.UserID() != "" || len(reader.calls) != 0 {
			t.Fatalf("mismatched read sealed or loaded untrusted facts: sealed=%#v calls=%#v", sealed, reader.calls)
		}
	})

	t.Run("write mismatch", func(t *testing.T) {
		plan, verificationContext, reader := validReplacementFixture()
		for index := range plan.Steps {
			plan.Steps[index].Arguments = bytes.ReplaceAll(plan.Steps[index].Arguments, []byte("AG-1042"), []byte("AG-1043"))
		}
		reader.orders["AG-1043"] = commerce.Order{ID: "AG-1043", UserID: verificationContext.UserID, SKU: "SKU-RED-42"}

		sealed, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
		requireErrorCode(t, result, "resolved_order_mismatch")
		if sealed.UserID() != "" || len(reader.calls) != 0 {
			t.Fatalf("mismatched write sealed or loaded untrusted facts: sealed=%#v calls=%#v", sealed, reader.calls)
		}
	})
}

func TestVerifierRequiresSafeTrustedOrderForOrderBoundPlans(t *testing.T) {
	for _, resolvedOrderID := range []string{"", "ag-1042", "AG-123", "AG-1042 extra"} {
		t.Run(resolvedOrderID, func(t *testing.T) {
			plan, verificationContext, reader := singleToolFixture("get_order", json.RawMessage(`{"order_id":"AG-1042"}`))
			verificationContext.ResolvedOrderID = resolvedOrderID
			_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
			requireErrorCode(t, result, "invalid_context")
			if len(reader.calls) != 0 {
				t.Fatalf("unsafe trusted order loaded facts: %#v", reader.calls)
			}
		})
	}
}

func TestVerifierBindsReplacementSKUToTrustedOrderFact(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	reader.orders[verificationContext.ResolvedOrderID] = commerce.Order{ID: verificationContext.ResolvedOrderID, UserID: verificationContext.UserID, SKU: "SKU-BLUE-99", PaidAmountCents: 12000}

	_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
	requireErrorCode(t, result, "sku_mismatch")
}

func TestVerifierRejectsSemanticallyDuplicateWriteArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments json.RawMessage
	}{
		{name: "escaped string", arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"SKU-RED-42","reason":"\u0064amaged"}`)},
		{name: "reordered object keys", arguments: json.RawMessage(`{"reason":"damaged","sku":"SKU-RED-42","order_id":"AG-1042"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, verificationContext, reader := duplicateReplacementFixture(tt.arguments)
			_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
			requireErrorCode(t, result, "duplicate_action")
		})
	}

	t.Run("equivalent integer representation", func(t *testing.T) {
		plan := refundPlan(500, true, "refund.created")
		duplicate := cloneStep(plan.Steps[1])
		duplicate.ID = "refund-again"
		duplicate.Arguments = json.RawMessage(`{"amount_cents":5e2,"order_id":"AG-1042"}`)
		duplicate.DependsOn = []string{"order", "refund"}
		plan.Steps = append(plan.Steps, duplicate)
		verificationContext := Context{UserID: "user-018", ResolvedOrderID: "AG-1042", Scopes: []string{"order:read", "refund:write"}, Now: verifierTestNow, Evidence: []policy.Evidence{refundEvidence()}}
		reader := &fakeFactReader{orders: map[string]commerce.Order{"AG-1042": {ID: "AG-1042", UserID: "user-018", PaidAmountCents: 12000}}}

		_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
		requireErrorCode(t, result, "duplicate_action")
	})
}

func TestVerifierAppliesExactRegistryJSONSchema(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		arguments json.RawMessage
	}{
		{name: "null string", tool: "get_order", arguments: json.RawMessage(`{"order_id":null}`)},
		{name: "duplicate object key", tool: "get_order", arguments: json.RawMessage(`{"order_id":"AG-1042","order_id":"AG-1042"}`)},
		{name: "fractional integer", tool: "issue_coupon", arguments: json.RawMessage(`{"amount_cents":1500.5,"reason":"service recovery"}`)},
		{name: "unknown property", tool: "check_inventory", arguments: json.RawMessage(`{"sku":"SKU-RED-42","warehouse":"attacker"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, verificationContext, reader := singleToolFixture(tt.tool, tt.arguments)
			_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
			requireErrorCode(t, result, "invalid_arguments")
		})
	}
}

func TestValidateArgumentsHonorsNumericAndNestedSchemaKeywords(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"amount_cents":{"type":"integer","minimum":1,"maximum":2000},
			"metadata":{
				"type":"object",
				"properties":{"reason":{"type":"string","minLength":3}},
				"required":["reason"],
				"additionalProperties":false
			}
		},
		"required":["amount_cents","metadata"],
		"additionalProperties":false
	}`)
	tests := []struct {
		name      string
		arguments string
		valid     bool
	}{
		{name: "minimum boundary", arguments: `{"amount_cents":1,"metadata":{"reason":"abc"}}`, valid: true},
		{name: "equivalent minimum", arguments: `{"amount_cents":1e0,"metadata":{"reason":"abc"}}`, valid: true},
		{name: "below minimum", arguments: `{"amount_cents":0,"metadata":{"reason":"abc"}}`},
		{name: "maximum boundary", arguments: `{"amount_cents":2000,"metadata":{"reason":"abc"}}`, valid: true},
		{name: "above maximum", arguments: `{"amount_cents":2001,"metadata":{"reason":"abc"}}`},
		{name: "nested minimum length", arguments: `{"amount_cents":1,"metadata":{"reason":"ab"}}`},
		{name: "nested unknown property", arguments: `{"amount_cents":1,"metadata":{"reason":"abc","trusted":true}}`},
		{name: "nested duplicate property", arguments: `{"amount_cents":1,"metadata":{"reason":"abc","reason":"def"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateArguments(json.RawMessage(tt.arguments), schema)
			if tt.valid && err != nil {
				t.Fatalf("validateArguments() error = %v, want valid", err)
			}
			if !tt.valid && err == nil {
				t.Fatal("validateArguments() error = nil, want schema rejection")
			}
		})
	}
}

func TestValidateArgumentsNormalizesExactJSONIntegers(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	valid := []struct {
		name      string
		number    string
		canonical string
	}{
		{name: "integer", number: "500", canonical: `{"value":500}`},
		{name: "decimal integer", number: "500.0", canonical: `{"value":500}`},
		{name: "exponent integer", number: "5e2", canonical: `{"value":500}`},
		{name: "maximum int64", number: "9223372036854775807", canonical: `{"value":9223372036854775807}`},
		{name: "minimum int64", number: "-9223372036854775808", canonical: `{"value":-9223372036854775808}`},
		{name: "maximum int64 exponent", number: "9.223372036854775807e18", canonical: `{"value":9223372036854775807}`},
		{name: "minimum int64 exponent", number: "-9.223372036854775808e18", canonical: `{"value":-9223372036854775808}`},
	}
	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			validated, err := validateArguments(json.RawMessage(`{"value":`+tt.number+`}`), schema)
			if err != nil {
				t.Fatalf("validateArguments() error = %v, want valid", err)
			}
			if validated.canonical != tt.canonical {
				t.Fatalf("canonical = %s, want %s", validated.canonical, tt.canonical)
			}
		})
	}

	invalid := []struct {
		name   string
		number string
	}{
		{name: "reviewer long fractional tail", number: "1500." + strings.Repeat("0", 80) + "1"},
		{name: "positive overflow", number: "9223372036854775808"},
		{name: "negative overflow", number: "-9223372036854775809"},
		{name: "exponent overflow", number: "9.223372036854775808e18"},
		{name: "fractional exponent", number: "5e-1"},
		{name: "truncated exponent", number: "1e"},
		{name: "leading zero", number: "01"},
		{name: "double sign", number: "--1"},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := validateArguments(json.RawMessage(`{"value":`+tt.number+`}`), schema); err == nil {
				t.Fatal("validateArguments() error = nil, want exact integer rejection")
			}
		})
	}
}

func TestVerifierReturnsStableRepairErrorForMissingScope(t *testing.T) {
	plan, verificationContext, reader := validReplacementFixture()
	verificationContext.Scopes = []string{"order:read", "shipment:read", "eligibility:read", "inventory:read"}

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
		UserID: "user-018", ResolvedOrderID: "AG-1042", Scopes: []string{"order:read", "refund:write"}, Now: verifierTestNow,
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
	verificationContext := Context{UserID: "user-018", ResolvedOrderID: "AG-1042", Scopes: []string{"coupon:write"}, Now: verifierTestNow, Evidence: []policy.Evidence{evidence}}
	_, result := New(nil).VerifyAndSeal(context.Background(), plan, verificationContext)
	requireErrorCode(t, result, "coupon_amount_exceeds_cap")
}

func TestVerifierRequiresWritePostconditionAndHighRiskApproval(t *testing.T) {
	t.Run("write postcondition", func(t *testing.T) {
		plan, verificationContext, reader := validReplacementFixture()
		plan.Steps[4].SuccessCondition = ""
		_, result := New(reader).VerifyAndSeal(context.Background(), plan, verificationContext)
		requireErrorCode(t, result, "invalid_postcondition")
	})

	t.Run("high risk approval", func(t *testing.T) {
		plan := refundPlan(500, false, "refund.created")
		verificationContext := Context{UserID: "user-018", ResolvedOrderID: "AG-1042", Scopes: []string{"order:read", "refund:write"}, Now: verifierTestNow, Evidence: []policy.Evidence{refundEvidence()}}
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
			{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read, SuccessCondition: "shipment.exists"},
			{ID: "eligibility", Tool: "check_eligibility", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read, SuccessCondition: "eligibility.eligible"},
			{ID: "inventory", Tool: "check_inventory", Arguments: json.RawMessage(`{"sku":"SKU-RED-42"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "inventory.available"},
			{ID: "replace", Tool: "create_replacement", Arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"SKU-RED-42","reason":"damaged"}`), DependsOn: []string{"shipment", "eligibility", "inventory"}, Risk: toolkit.Write, SuccessCondition: "replacement.created"},
		},
	}
	verificationContext := Context{
		UserID: "user-018", ResolvedOrderID: "AG-1042",
		Scopes: []string{"order:read", "shipment:read", "eligibility:read", "inventory:read", "replacement:write"},
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

func duplicateReplacementFixture(arguments json.RawMessage) (agent.ActionPlan, Context, *fakeFactReader) {
	plan, verificationContext, reader := validReplacementFixture()
	duplicate := cloneStep(plan.Steps[4])
	duplicate.ID = "replace-again"
	duplicate.Arguments = arguments
	plan.Steps = append(plan.Steps, duplicate)

	capCents := int64(2000)
	couponEvidence := baseEvidence("customer_care", "v1", "Coupon compensation", toolkit.HighRiskWrite)
	couponEvidence.MaxCouponCents = &capCents
	plan.PolicyRefs = append(plan.PolicyRefs, couponEvidence.CitationID)
	verificationContext.Evidence = append(verificationContext.Evidence, couponEvidence)
	verificationContext.Scopes = append(verificationContext.Scopes, "coupon:write")
	plan.Steps = append(plan.Steps, agent.Step{
		ID: "coupon", Tool: "issue_coupon", Arguments: json.RawMessage(`{"amount_cents":1000,"reason":"service recovery"}`),
		DependsOn: []string{"replace", "replace-again"}, Risk: toolkit.HighRiskWrite,
		SuccessCondition: "coupon.created", ApprovalRequired: true,
	})
	return plan, verificationContext, reader
}

func singleToolFixture(tool string, arguments json.RawMessage) (agent.ActionPlan, Context, *fakeFactReader) {
	contract, ok := toolkit.LookupContract(tool)
	if !ok {
		panic("test fixture uses unknown tool " + tool)
	}
	plan := agent.ActionPlan{Goal: "validate arguments", Steps: []agent.Step{{
		ID: "step", Tool: tool, Arguments: arguments, DependsOn: []string{}, Risk: contract.Risk,
		SuccessCondition: firstSuccessCondition(tool), ApprovalRequired: contract.Risk == toolkit.HighRiskWrite,
	}}}
	verificationContext := Context{UserID: "user-018", ResolvedOrderID: "AG-1042", Scopes: []string{contract.Scope}, Now: verifierTestNow}
	reader := &fakeFactReader{orders: map[string]commerce.Order{"AG-1042": {ID: "AG-1042", UserID: "user-018", PaidAmountCents: 12000}}}
	if policyID, needsEvidence := requiredPolicyByTool[tool]; needsEvidence {
		requirement := policyRequirements[policyID]
		evidence := baseEvidence(policyID, "v1", "Applicable rule", requirement.risk)
		if policyID == "customer_care" {
			capCents := int64(2000)
			evidence.MaxCouponCents = &capCents
		}
		plan.PolicyRefs = []string{evidence.CitationID}
		verificationContext.Evidence = []policy.Evidence{evidence}
	}
	return plan, verificationContext, reader
}

func firstSuccessCondition(tool string) string {
	for condition := range successConditions[tool] {
		return condition
	}
	return ""
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
