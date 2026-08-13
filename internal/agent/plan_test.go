package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

func TestDecodeActionPlanAcceptsCanonicalReplacementPlan(t *testing.T) {
	plan, err := DecodeActionPlan(validReplacementPlanJSON())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Goal != "replace damaged item" || len(plan.PolicyRefs) != 1 || len(plan.Steps) != 5 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Steps[1].Tool != "get_shipment" || string(plan.Steps[1].Arguments) != `{"order_id":"AG-1042"}` {
		t.Fatalf("shipment step = %#v", plan.Steps[1])
	}
	if plan.Steps[4].Tool != "create_replacement" || plan.Steps[4].Risk != toolkit.Write {
		t.Fatalf("terminal step = %#v", plan.Steps[4])
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeActionPlan(encoded); err != nil {
		t.Fatalf("round-trip DecodeActionPlan() error = %v", err)
	}
}

func TestDecodeActionPlanRejectsNonStrictJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "unknown plan field", data: `{"goal":"lookup","policy_refs":[],"steps":[],"extra":true}`},
		{name: "unknown step field", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false,"extra":true}]}`},
		{name: "duplicate plan field", data: `{"goal":"lookup","goal":"refund","policy_refs":[],"steps":[]}`},
		{name: "duplicate argument field", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":{"order_id":"AG-1042","order_id":"AG-9001"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
		{name: "non object arguments", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":[],"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
		{name: "null arguments", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":null,"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
		{name: "unknown risk", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"admin","success_condition":"order.exists","approval_required":false}]}`},
		{name: "unknown tool", data: `{"goal":"delete","policy_refs":[],"steps":[{"id":"s1","tool":"delete_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"write","success_condition":"order.deleted","approval_required":false}]}`},
		{name: "trailing value", data: `{"goal":"lookup","policy_refs":[],"steps":[]} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeActionPlan([]byte(tt.data)); err == nil {
				t.Fatal("DecodeActionPlan() error = nil")
			}
		})
	}
}

func TestDecodeActionPlanRejectsMoreThanTwelveSteps(t *testing.T) {
	steps := make([]string, 13)
	for index := range steps {
		steps[index] = fmt.Sprintf(`{"id":"s%d","tool":"get_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}`, index)
	}
	data := fmt.Sprintf(`{"goal":"lookup","policy_refs":[],"steps":[%s]}`, strings.Join(steps, ","))
	if _, err := DecodeActionPlan([]byte(data)); err == nil {
		t.Fatal("DecodeActionPlan() error = nil")
	}
}

func TestDecodeActionPlanRejectsExecutableAndTrustedArgumentFieldsRecursively(t *testing.T) {
	for _, field := range []string{
		"code", "sql", "shell", "user_id", "run_id", "step_id", "scopes", "scope",
		"idempotency_key", "identity", "call_context",
	} {
		t.Run(field, func(t *testing.T) {
			data := fmt.Sprintf(`{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":{"nested":{"deeper":{"%s":"attacker-controlled"}},"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`, field)
			if _, err := DecodeActionPlan([]byte(data)); err == nil {
				t.Fatal("DecodeActionPlan() error = nil")
			}
		})
	}
}

func TestDecodeActionPlanRejectsCaseVariantAliases(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "plan alias", data: `{"Goal":"lookup","policy_refs":[],"steps":[]}`},
		{name: "step alias", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","Tool":"get_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
		{name: "mixed canonical and alias", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","Tool":"issue_refund","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
		{name: "arguments alias hides recursive trusted metadata", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","Arguments":{"nested":{"User_ID":"attacker"},"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
		{name: "mixed arguments alias hides recursive trusted metadata", data: `{"goal":"lookup","policy_refs":[],"steps":[{"id":"s1","tool":"get_order","arguments":{"order_id":"AG-1042"},"Arguments":{"nested":[{"Scopes":["attacker:write"]}]},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeActionPlan([]byte(tt.data)); err == nil {
				t.Fatal("DecodeActionPlan() error = nil")
			}
		})
	}
}

func validReplacementPlanJSON() []byte {
	return []byte(`{
		"goal":"replace damaged item",
		"policy_refs":["damaged_goods:v3:Replacement eligibility:10-120"],
			"steps":[
				{"id":"order","tool":"get_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false},
				{"id":"shipment","tool":"get_shipment","arguments":{"order_id":"AG-1042"},"depends_on":["order"],"risk":"read","success_condition":"shipment.exists","approval_required":false},
				{"id":"eligibility","tool":"check_eligibility","arguments":{"order_id":"AG-1042"},"depends_on":["order"],"risk":"read","success_condition":"eligibility.eligible","approval_required":false},
				{"id":"inventory","tool":"check_inventory","arguments":{"sku":"SKU-RED-42"},"depends_on":[],"risk":"read","success_condition":"inventory.available","approval_required":false},
				{"id":"replace","tool":"create_replacement","arguments":{"order_id":"AG-1042","sku":"SKU-RED-42","reason":"damaged"},"depends_on":["shipment","eligibility","inventory"],"risk":"write","success_condition":"replacement.created","approval_required":false}
		]
	}`)
}
