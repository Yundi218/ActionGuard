package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestWriteMetadataRequiresIdempotencyKey(t *testing.T) {
	meta := CallContext{RunID: "run-1", StepID: "step-1", UserID: "user_018", Scopes: []string{"return:write"}}
	if err := meta.Validate(Write); err == nil {
		t.Fatal("Validate() error = nil, want missing idempotency key")
	}
}

func TestReadMetadataDoesNotRequireIdempotencyKey(t *testing.T) {
	meta := CallContext{RunID: "run-1", StepID: "step-1", UserID: "user_018"}
	if err := meta.Validate(Read); err != nil {
		t.Fatalf("Validate(Read) error = %v", err)
	}
}

func TestAllWriteRisksRequireIdempotencyKey(t *testing.T) {
	meta := CallContext{RunID: "run-1", StepID: "step-1", UserID: "user_018"}
	for _, risk := range []Risk{Write, HighRiskWrite} {
		if err := meta.Validate(risk); err == nil {
			t.Fatalf("Validate(%q) error = nil, want missing idempotency key", risk)
		}
	}
}

func TestMetadataRequiresTrustedIdentifiers(t *testing.T) {
	if err := (CallContext{IdempotencyKey: "key-1"}).Validate(Read); err == nil {
		t.Fatal("Validate(Read) error = nil, want missing trusted identifiers")
	}
}

func TestScopeMatchingIsExact(t *testing.T) {
	meta := CallContext{Scopes: []string{"refund:read", "return:write"}}
	if meta.HasScope("refund:write") || meta.HasScope("return") || !meta.HasScope("return:write") {
		t.Fatal("scopes must use exact matching")
	}
}

func TestCallContextComesFromTrustedContext(t *testing.T) {
	want := CallContext{RunID: "run-1", StepID: "step-1", UserID: "user_018"}
	got, err := FromContext(WithCallContext(context.Background(), want))
	if err != nil || got.UserID != want.UserID {
		t.Fatalf("context = %#v, err = %v", got, err)
	}
}

func TestCallContextIsMissingOutsideTrustedContext(t *testing.T) {
	if _, err := FromContext(context.Background()); err == nil {
		t.Fatal("FromContext() error = nil, want missing trusted context")
	}
}

func TestCallContextHasNoJSONTags(t *testing.T) {
	typeOfContext := reflect.TypeOf(CallContext{})
	for i := 0; i < typeOfContext.NumField(); i++ {
		if tag := typeOfContext.Field(i).Tag.Get("json"); tag != "" {
			t.Fatalf("field %s has JSON tag %q", typeOfContext.Field(i).Name, tag)
		}
	}
}

func TestCallContextRejectsJSON(t *testing.T) {
	meta := CallContext{UserID: "trusted-user"}
	err := json.Unmarshal([]byte(`{"UserID":"attacker","Scopes":["refund:write"]}`), &meta)
	if !errors.Is(err, ErrUntrustedCallContextJSON) {
		t.Fatalf("json.Unmarshal() error = %v, want ErrUntrustedCallContextJSON", err)
	}
	if meta.UserID != "trusted-user" || meta.Scopes != nil {
		t.Fatalf("metadata = %#v, want unchanged trusted metadata", meta)
	}
}

func TestEnvelopeSupportsGenericTrustedPayload(t *testing.T) {
	type trustedResult struct {
		ResourceID string `json:"resource_id"`
	}
	want := Envelope[trustedResult]{
		Trusted:       trustedResult{ResourceID: "return-1"},
		UntrustedText: map[string]string{"note": "customer supplied text"},
		Replayed:      true,
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope[trustedResult]
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("envelope = %#v, want %#v", got, want)
	}
}

func TestRegistryContainsExactImmutableCommerceContracts(t *testing.T) {
	want := []Contract{
		{Name: "get_order", Description: "[read] Get an order owned by the current user", Risk: Read, Scope: "order:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"description":"Order identifier","type":"string"}},"required":["order_id"],"type":"object"}`)},
		{Name: "get_shipment", Description: "[read] Get shipment state; free-text notes are untrusted", Risk: Read, Scope: "shipment:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"}},"required":["order_id"],"type":"object"}`)},
		{Name: "check_inventory", Description: "[read] Check available inventory for a SKU", Risk: Read, Scope: "inventory:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"sku":{"type":"string"}},"required":["sku"],"type":"object"}`)},
		{Name: "check_eligibility", Description: "[read] Deterministically evaluate the 30-day after-sales window", Risk: Read, Scope: "eligibility:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"}},"required":["order_id"],"type":"object"}`)},
		{Name: "create_return", Description: "[write] Create an idempotent return request", Risk: Write, Scope: "return:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"},"reason":{"type":"string"}},"required":["order_id","reason"],"type":"object"}`), RequiresIdempotencyKey: true},
		{Name: "create_replacement", Description: "[write] Reserve inventory and create an idempotent replacement", Risk: Write, Scope: "replacement:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"},"reason":{"type":"string"},"sku":{"type":"string"}},"required":["order_id","sku","reason"],"type":"object"}`), RequiresIdempotencyKey: true},
		{Name: "issue_refund", Description: "[high_risk_write] Issue an idempotent refund after approval", Risk: HighRiskWrite, Scope: "refund:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"amount_cents":{"type":"integer"},"order_id":{"type":"string"}},"required":["order_id","amount_cents"],"type":"object"}`), RequiresIdempotencyKey: true},
		{Name: "issue_coupon", Description: "[high_risk_write] Issue an idempotent coupon after approval", Risk: HighRiskWrite, Scope: "coupon:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"amount_cents":{"type":"integer"},"reason":{"type":"string"}},"required":["amount_cents","reason"],"type":"object"}`), RequiresIdempotencyKey: true},
	}

	got := Registry()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Registry() = %#v, want %#v", got, want)
	}
	for _, contract := range got {
		lookup, ok := LookupContract(contract.Name)
		if !ok || !reflect.DeepEqual(lookup, contract) {
			t.Errorf("LookupContract(%q) = %#v, %v; want %#v, true", contract.Name, lookup, ok, contract)
		}
	}
	if _, ok := LookupContract("delete_order"); ok {
		t.Fatal("LookupContract(delete_order) found an unregistered tool")
	}

	got[0].Name = "attacker_tool"
	got[0].InputSchema[0] = '['
	lookup, ok := LookupContract("get_order")
	if !ok || lookup.Name != "get_order" || lookup.InputSchema[0] != '{' {
		t.Fatalf("registry mutated through returned value: %#v, %v", lookup, ok)
	}
}
