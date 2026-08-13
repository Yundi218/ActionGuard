package agent

import (
	"encoding/json"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

var actionPlanSchema = buildActionPlanSchema()

func ActionPlanSchema() json.RawMessage {
	return append(json.RawMessage(nil), actionPlanSchema...)
}

func buildActionPlanSchema() json.RawMessage {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"goal", "policy_refs", "steps"},
		"properties": map[string]any{
			"goal": map[string]any{"type": "string", "minLength": 1},
			"policy_refs": map[string]any{
				"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "uniqueItems": true,
			},
			"steps": map[string]any{
				"type": "array", "maxItems": MaxSteps,
				"items": map[string]any{"anyOf": actionStepSchemas()},
			},
		},
	}
	data, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return data
}

func actionStepSchemas() []any {
	contracts := toolkit.Registry()
	branches := make([]any, 0, len(contracts))
	for _, contract := range contracts {
		var arguments any
		if err := json.Unmarshal(contract.InputSchema, &arguments); err != nil {
			panic(err)
		}
		approval := map[string]any{"type": "boolean"}
		if contract.Risk == toolkit.HighRiskWrite {
			approval["const"] = true
		}
		branches = append(branches, map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required": []string{
				"id", "tool", "arguments", "depends_on", "risk", "success_condition", "approval_required",
			},
			"properties": map[string]any{
				"id":        map[string]any{"type": "string", "minLength": 1},
				"tool":      map[string]any{"type": "string", "const": contract.Name},
				"arguments": arguments,
				"depends_on": map[string]any{
					"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "maxItems": MaxSteps, "uniqueItems": true,
				},
				"risk":              map[string]any{"type": "string", "const": string(contract.Risk)},
				"success_condition": map[string]any{"type": "string"},
				"approval_required": approval,
			},
		})
	}
	return branches
}
