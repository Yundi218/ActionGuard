package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/Yundi218/ActionGuard/internal/toolkit"
)

const MaxSteps = 12

type ActionPlan struct {
	Goal       string   `json:"goal"`
	PolicyRefs []string `json:"policy_refs"`
	Steps      []Step   `json:"steps"`
}

type Step struct {
	ID               string          `json:"id"`
	Tool             string          `json:"tool"`
	Arguments        json.RawMessage `json:"arguments"`
	DependsOn        []string        `json:"depends_on"`
	Risk             toolkit.Risk    `json:"risk"`
	SuccessCondition string          `json:"success_condition"`
	ApprovalRequired bool            `json:"approval_required"`
}

type actionPlanWire struct {
	Goal       *string     `json:"goal"`
	PolicyRefs *[]string   `json:"policy_refs"`
	Steps      *[]stepWire `json:"steps"`
}

type stepWire struct {
	ID               *string          `json:"id"`
	Tool             *string          `json:"tool"`
	Arguments        *json.RawMessage `json:"arguments"`
	DependsOn        *[]string        `json:"depends_on"`
	Risk             *toolkit.Risk    `json:"risk"`
	SuccessCondition *string          `json:"success_condition"`
	ApprovalRequired *bool            `json:"approval_required"`
}

func DecodeActionPlan(data []byte) (ActionPlan, error) {
	if err := validateJSONStructure(data); err != nil {
		return ActionPlan{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire actionPlanWire
	if err := decoder.Decode(&wire); err != nil {
		return ActionPlan{}, fmt.Errorf("decode action plan: %w", err)
	}
	if err := requireEOF(decoder); err != nil {
		return ActionPlan{}, err
	}
	if wire.Goal == nil || wire.PolicyRefs == nil || wire.Steps == nil {
		return ActionPlan{}, errors.New("decode action plan: goal, policy_refs, and steps are required")
	}
	if len(*wire.Steps) > MaxSteps {
		return ActionPlan{}, fmt.Errorf("decode action plan: steps exceeds maximum of %d", MaxSteps)
	}

	plan := ActionPlan{Goal: *wire.Goal, PolicyRefs: append([]string{}, (*wire.PolicyRefs)...), Steps: make([]Step, len(*wire.Steps))}
	for index, step := range *wire.Steps {
		if step.ID == nil || step.Tool == nil || step.Arguments == nil || step.DependsOn == nil || step.Risk == nil || step.SuccessCondition == nil || step.ApprovalRequired == nil {
			return ActionPlan{}, fmt.Errorf("decode action plan: all fields are required for step %d", index)
		}
		arguments := bytes.TrimSpace(*step.Arguments)
		if len(arguments) == 0 || arguments[0] != '{' {
			return ActionPlan{}, fmt.Errorf("decode action plan: step %d arguments must be an object", index)
		}
		if !step.Risk.Valid() {
			return ActionPlan{}, fmt.Errorf("decode action plan: step %d has unknown risk %q", index, *step.Risk)
		}
		if _, ok := toolkit.LookupContract(*step.Tool); !ok {
			return ActionPlan{}, fmt.Errorf("decode action plan: step %d has unknown tool %q", index, *step.Tool)
		}
		plan.Steps[index] = Step{
			ID:               *step.ID,
			Tool:             *step.Tool,
			Arguments:        append(json.RawMessage(nil), arguments...),
			DependsOn:        append([]string{}, (*step.DependsOn)...),
			Risk:             *step.Risk,
			SuccessCondition: *step.SuccessCondition,
			ApprovalRequired: *step.ApprovalRequired,
		}
	}
	return plan, nil
}

func validateJSONStructure(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode action plan: %w", err)
	}
	if first != json.Delim('{') {
		return errors.New("decode action plan: plan must be an object")
	}
	if err := validateObject(decoder, planObject, false); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	return nil
}

type objectKind uint8

const (
	genericObject objectKind = iota
	planObject
	stepObject
)

func validateValue(decoder *json.Decoder, inArguments bool) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode action plan: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		return validateObject(decoder, genericObject, inArguments)
	case '[':
		for decoder.More() {
			if err := validateValue(decoder, inArguments); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return fmt.Errorf("decode action plan: %w", err)
		}
		return nil
	default:
		return errors.New("decode action plan: unexpected JSON delimiter")
	}
}

func validateObject(decoder *json.Decoder, kind objectKind, inArguments bool) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode action plan: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("decode action plan: object key must be a string")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("decode action plan: duplicate field %q", key)
		}
		seen[key] = struct{}{}
		if !exactMember(kind, key) {
			return fmt.Errorf("decode action plan: unknown field %q", key)
		}
		if inArguments && forbiddenArgumentField(key) {
			return fmt.Errorf("decode action plan: forbidden argument field %q", key)
		}
		if kind == planObject && key == "steps" {
			if err := validateSteps(decoder); err != nil {
				return err
			}
			continue
		}
		childInArguments := inArguments || kind == stepObject && key == "arguments"
		if err := validateValue(decoder, childInArguments); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode action plan: %w", err)
	}
	return nil
}

func validateSteps(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode action plan: %w", err)
	}
	if token != json.Delim('[') {
		return errors.New("decode action plan: steps must be an array")
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode action plan: %w", err)
		}
		if token != json.Delim('{') {
			return errors.New("decode action plan: every step must be an object")
		}
		if err := validateObject(decoder, stepObject, false); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode action plan: %w", err)
	}
	return nil
}

func exactMember(kind objectKind, key string) bool {
	switch kind {
	case planObject:
		return key == "goal" || key == "policy_refs" || key == "steps"
	case stepObject:
		switch key {
		case "id", "tool", "arguments", "depends_on", "risk", "success_condition", "approval_required":
			return true
		default:
			return false
		}
	default:
		return true
	}
}

func forbiddenArgumentField(field string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, field)
	switch normalized {
	case "code", "sql", "shell", "userid", "runid", "stepid", "scopes", "scope", "idempotencykey", "identity", "callcontext":
		return true
	default:
		return false
	}
}

func requireEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode action plan: %w", err)
	}
	return errors.New("decode action plan: multiple JSON values")
}
