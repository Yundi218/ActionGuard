package toolkit

import (
	"context"
	"encoding/json"
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
