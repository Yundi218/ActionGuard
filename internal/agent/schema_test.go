package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestActionPlanSchemaIsStrictAndRoundTripsReplacementPlan(t *testing.T) {
	schemaJSON := ActionPlanSchema()
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}

	var planJSON any
	if err := json.Unmarshal(validReplacementPlanJSON(), &planJSON); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(planJSON); err != nil {
		t.Fatalf("valid replacement plan failed schema validation: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(schemaJSON, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["additionalProperties"] != false {
		t.Fatalf("plan additionalProperties = %#v, want false", raw["additionalProperties"])
	}
	steps := raw["properties"].(map[string]any)["steps"].(map[string]any)
	if steps["maxItems"] != float64(12) {
		t.Fatalf("steps maxItems = %#v, want 12", steps["maxItems"])
	}
	branches, ok := steps["items"].(map[string]any)["anyOf"].([]any)
	if !ok {
		t.Fatal("step schema does not branch over the registered tool contracts")
	}
	if len(branches) != 8 {
		t.Fatalf("step branches = %d, want eight exact tools", len(branches))
	}
	contracts := toolkit.Registry()
	for index, contract := range contracts {
		branch := branches[index].(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("branch %q additionalProperties = %#v, want false", contract.Name, branch["additionalProperties"])
		}
		properties := branch["properties"].(map[string]any)
		if properties["tool"].(map[string]any)["const"] != contract.Name {
			t.Fatalf("branch %d tool = %#v, want %q", index, properties["tool"], contract.Name)
		}
		if properties["risk"].(map[string]any)["const"] != string(contract.Risk) {
			t.Fatalf("branch %q risk = %#v, want %q", contract.Name, properties["risk"], contract.Risk)
		}
		gotArguments, err := json.Marshal(properties["arguments"])
		if err != nil {
			t.Fatal(err)
		}
		var registryArguments any
		if err := json.Unmarshal(contract.InputSchema, &registryArguments); err != nil {
			t.Fatal(err)
		}
		wantArguments, err := json.Marshal(registryArguments)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotArguments, wantArguments) {
			t.Fatalf("branch %q arguments = %s, want registry schema %s", contract.Name, gotArguments, wantArguments)
		}
		if contract.Risk == toolkit.HighRiskWrite && properties["approval_required"].(map[string]any)["const"] != true {
			t.Fatalf("branch %q does not require approval: %#v", contract.Name, properties["approval_required"])
		}
	}
}

func TestActionPlanSchemaReturnsDefensiveBytes(t *testing.T) {
	first := ActionPlanSchema()
	first[0] = '['
	second := ActionPlanSchema()
	if len(second) == 0 || second[0] != '{' {
		t.Fatalf("ActionPlanSchema() was mutated through returned bytes: %s", second)
	}
}
