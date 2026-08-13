package verifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	supportedRegion   = "CN"
	supportedCategory = "electronics"
)

type Context struct {
	UserID          string
	ResolvedOrderID string
	Scopes          []string
	Now             time.Time
	Evidence        []policy.Evidence
}

var safeOrderIDPattern = regexp.MustCompile(`^AG-[0-9]{4,}$`)

type FactReader interface {
	GetOrder(context.Context, string, string) (commerce.Order, error)
}

type Error struct {
	Code    string `json:"code"`
	StepID  string `json:"step_id"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Result struct {
	Valid  bool    `json:"valid"`
	Errors []Error `json:"errors,omitempty"`
}

type VerifiedPlan struct {
	plan        agent.ActionPlan
	userID      string
	scopes      []string
	scopeDigest [sha256.Size]byte
}

func (plan VerifiedPlan) Plan() agent.ActionPlan {
	return clonePlan(plan.plan)
}

func (plan VerifiedPlan) UserID() string {
	return plan.userID
}

func (plan VerifiedPlan) Scopes() []string {
	return append([]string{}, plan.scopes...)
}

func (plan VerifiedPlan) ScopeDigest() [sha256.Size]byte {
	return plan.scopeDigest
}

type Verifier struct {
	facts FactReader
}

func New(facts FactReader) *Verifier {
	return &Verifier{facts: facts}
}

type verificationState struct {
	plan               agent.ActionPlan
	context            Context
	contracts          []toolkit.Contract
	contractsByName    map[string]toolkit.Contract
	scopeSet           map[string]struct{}
	parsedArguments    []map[string]any
	evidenceByID       map[string]policy.Evidence
	applicableEvidence map[string][]policy.Evidence
	errors             []Error
}

func (verifier *Verifier) VerifyAndSeal(ctx context.Context, plan agent.ActionPlan, verificationContext Context) (VerifiedPlan, Result) {
	planSnapshot := clonePlan(plan)
	canonicalScopes := canonicalScopes(verificationContext.Scopes)
	state := verificationState{
		plan:               planSnapshot,
		context:            verificationContext,
		contracts:          toolkit.Registry(),
		scopeSet:           make(map[string]struct{}, len(canonicalScopes)),
		parsedArguments:    make([]map[string]any, len(planSnapshot.Steps)),
		evidenceByID:       make(map[string]policy.Evidence, len(verificationContext.Evidence)),
		applicableEvidence: make(map[string][]policy.Evidence),
	}
	state.contractsByName = make(map[string]toolkit.Contract, len(state.contracts))
	for _, contract := range state.contracts {
		state.contractsByName[contract.Name] = contract
	}
	for _, scope := range canonicalScopes {
		state.scopeSet[scope] = struct{}{}
	}
	if strings.TrimSpace(verificationContext.UserID) == "" {
		state.add("invalid_context", "", "user_id", "verified user id is required")
	}
	for _, scope := range canonicalScopes {
		if strings.ContainsRune(scope, '\x00') {
			state.add("invalid_context", "", "scopes", "scope values cannot contain NUL")
			break
		}
	}
	state.validateEvidence()
	state.validateContracts()
	state.validateGraph()
	state.validateBindings()
	state.validateWriteEvidence()
	state.validateFactsAndAmounts(ctx, verifier)

	if len(state.errors) != 0 {
		return VerifiedPlan{}, Result{Errors: state.errors}
	}
	digest := sha256.Sum256([]byte(strings.Join(canonicalScopes, "\x00")))
	return VerifiedPlan{
		plan: clonePlan(planSnapshot), userID: verificationContext.UserID,
		scopes: append([]string{}, canonicalScopes...), scopeDigest: digest,
	}, Result{Valid: true}
}

func (state *verificationState) validateEvidence() {
	for _, evidence := range state.context.Evidence {
		if evidence.CitationID == "" {
			continue
		}
		if _, exists := state.evidenceByID[evidence.CitationID]; exists {
			state.add("duplicate_evidence", "", "evidence", "evidence citation ids must be unique")
			continue
		}
		state.evidenceByID[evidence.CitationID] = evidence
	}

	seenRefs := make(map[string]struct{}, len(state.plan.PolicyRefs))
	for _, reference := range state.plan.PolicyRefs {
		if _, exists := seenRefs[reference]; exists {
			state.add("duplicate_citation", "", "policy_refs", "policy references must be unique")
			continue
		}
		seenRefs[reference] = struct{}{}
		evidence, exists := state.evidenceByID[reference]
		if !exists {
			state.add("citation_not_found", "", "policy_refs", "policy reference must exactly match retrieved evidence")
			continue
		}
		requirement, knownPolicy := policyRequirements[evidence.PolicyID]
		if !knownPolicy || !evidenceApplicable(evidence, state.context.Now, requirement) {
			state.add("evidence_not_applicable", "", "policy_refs", "cited evidence is not applicable to this action context")
			continue
		}
		state.applicableEvidence[evidence.PolicyID] = append(state.applicableEvidence[evidence.PolicyID], evidence)
	}
}

func (state *verificationState) validateContracts() {
	if len(state.plan.Steps) > agent.MaxSteps {
		state.add("too_many_steps", "", "steps", "plan exceeds the maximum step count")
	}
	seenActions := make(map[string]struct{}, len(state.plan.Steps))
	for index, step := range state.plan.Steps {
		contract, exists := state.contractsByName[step.Tool]
		if !exists {
			state.add("unknown_tool", step.ID, "tool", "tool is not registered")
			continue
		}
		arguments, err := validateArguments(step.Arguments, contract.InputSchema)
		if err != nil {
			state.add("invalid_arguments", step.ID, "arguments", "arguments do not match the registered tool schema")
		} else {
			state.parsedArguments[index] = arguments.values
			actionKey := step.Tool + "\x00" + arguments.canonical
			if _, duplicate := seenActions[actionKey]; duplicate {
				state.add("duplicate_action", step.ID, "arguments", "duplicate tool and arguments are not allowed")
			} else {
				seenActions[actionKey] = struct{}{}
			}
		}
		if step.Risk != contract.Risk {
			state.add("risk_mismatch", step.ID, "risk", "step risk must exactly match the registered tool risk")
		}
		if _, ok := state.scopeSet[contract.Scope]; !ok {
			state.add("missing_scope", step.ID, "scope", "required tool scope is missing")
		}
		if !validSuccessCondition(step.Tool, step.SuccessCondition) {
			if contract.Risk != toolkit.Read || step.SuccessCondition != "" {
				state.add("invalid_postcondition", step.ID, "success_condition", "success condition is not allowlisted for this tool")
			}
		}
		if contract.Risk == toolkit.HighRiskWrite && !step.ApprovalRequired {
			state.add("approval_required", step.ID, "approval_required", "high-risk writes must declare approval intent")
		}
	}
}

func (state *verificationState) validateGraph() {
	if len(state.plan.Steps) == 0 {
		state.add("terminal_count", "", "steps", "plan must have exactly one terminal step")
		return
	}
	byID := make(map[string]int, len(state.plan.Steps))
	duplicateID := false
	for index, step := range state.plan.Steps {
		if step.ID == "" {
			state.add("invalid_step_id", "", "id", "step id is required")
			duplicateID = true
			continue
		}
		if _, exists := byID[step.ID]; exists {
			state.add("duplicate_step_id", step.ID, "id", "step ids must be unique")
			duplicateID = true
			continue
		}
		byID[step.ID] = index
	}
	if duplicateID {
		return
	}

	dependents := make([]int, len(state.plan.Steps))
	for _, step := range state.plan.Steps {
		seenDependencies := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if _, duplicate := seenDependencies[dependency]; duplicate {
				state.add("duplicate_dependency", step.ID, "depends_on", "dependencies must be unique")
				continue
			}
			seenDependencies[dependency] = struct{}{}
			if dependency == step.ID {
				state.add("self_dependency", step.ID, "depends_on", "a step cannot depend on itself")
				continue
			}
			dependencyIndex, exists := byID[dependency]
			if !exists {
				state.add("unknown_dependency", step.ID, "depends_on", "dependency must reference an existing step")
				continue
			}
			dependents[dependencyIndex]++
		}
	}
	if cycleStep := dependencyCycle(state.plan.Steps, byID); cycleStep != "" {
		state.add("dependency_cycle", cycleStep, "depends_on", "dependency graph must be acyclic")
	}

	terminals := make([]int, 0, 1)
	for index, dependentCount := range dependents {
		if dependentCount == 0 {
			terminals = append(terminals, index)
		}
	}
	if len(terminals) != 1 {
		state.add("terminal_count", "", "steps", "plan must have exactly one terminal step")
		return
	}
	terminal := state.plan.Steps[terminals[0]]
	rules, exists := prerequisiteRules[terminal.Tool]
	if !exists {
		return
	}

	for _, step := range state.plan.Steps {
		allowedDependencies, allowedTool := rules.allowedDependencies[step.Tool]
		if !allowedTool {
			state.add("prerequisite_not_allowed", step.ID, "tool", "step is outside the terminal tool prerequisite graph")
			continue
		}
		dependencyTools := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			dependencyIndex, exists := byID[dependency]
			if !exists {
				continue
			}
			dependencyTool := state.plan.Steps[dependencyIndex].Tool
			dependencyTools[dependencyTool] = struct{}{}
			if _, allowed := allowedDependencies[dependencyTool]; !allowed {
				state.add("prerequisite_not_allowed", step.ID, "depends_on", "dependency edge is outside the terminal tool prerequisite graph")
			}
		}
		for _, requiredTool := range rules.requiredDependencies[step.Tool] {
			if _, present := dependencyTools[requiredTool]; !present {
				state.add("missing_prerequisite", step.ID, "depends_on", "required prerequisite is missing")
			}
		}
	}

	ancestors := make(map[string]struct{}, len(state.plan.Steps))
	collectAncestors(terminal.ID, state.plan.Steps, byID, ancestors)
	for _, step := range state.plan.Steps {
		if _, ok := ancestors[step.ID]; !ok {
			state.add("not_terminal_ancestor", step.ID, "depends_on", "every step must be an ancestor of the terminal step")
		}
	}
}

func (state *verificationState) validateWriteEvidence() {
	for _, step := range state.plan.Steps {
		contract, exists := state.contractsByName[step.Tool]
		if !exists || contract.Risk == toolkit.Read {
			continue
		}
		policyID := requiredPolicyByTool[step.Tool]
		if len(state.applicableEvidence[policyID]) == 0 {
			state.add("missing_citation", step.ID, "policy_refs", "write step requires exact applicable policy evidence")
		}
	}
}

func (state *verificationState) validateBindings() {
	byID := make(map[string]int, len(state.plan.Steps))
	for index, step := range state.plan.Steps {
		if step.ID == "" {
			continue
		}
		if _, duplicate := byID[step.ID]; duplicate {
			continue
		}
		byID[step.ID] = index
	}

	for replacementIndex, replacement := range state.plan.Steps {
		if replacement.Tool != "create_replacement" || state.parsedArguments[replacementIndex] == nil {
			continue
		}
		replacementSKU, ok := stringArgument(state.parsedArguments[replacementIndex], "sku")
		if !ok {
			continue
		}
		ancestors := make(map[string]struct{}, len(state.plan.Steps))
		collectAncestors(replacement.ID, state.plan.Steps, byID, ancestors)
		for ancestorID := range ancestors {
			ancestorIndex, exists := byID[ancestorID]
			if !exists || state.plan.Steps[ancestorIndex].Tool != "check_inventory" || state.parsedArguments[ancestorIndex] == nil {
				continue
			}
			inventorySKU, ok := stringArgument(state.parsedArguments[ancestorIndex], "sku")
			if ok && inventorySKU != replacementSKU {
				state.add("sku_mismatch", replacement.ID, "arguments.sku", "replacement sku must exactly match every prerequisite inventory check")
			}
		}
	}
}

func (state *verificationState) validateFactsAndAmounts(ctx context.Context, verifier *Verifier) {
	type factResult struct {
		order commerce.Order
		err   error
	}
	orderBound := false
	trustedOrderMatches := true
	firstOrderID := ""
	for index, step := range state.plan.Steps {
		arguments := state.parsedArguments[index]
		if arguments == nil {
			continue
		}
		orderID, hasOrder := stringArgument(arguments, "order_id")
		if hasOrder {
			orderBound = true
			if firstOrderID == "" {
				firstOrderID = orderID
			} else if orderID != firstOrderID {
				state.add("order_mismatch", step.ID, "arguments.order_id", "all order-bound steps must target the same order")
			}
			if orderID != state.context.ResolvedOrderID {
				trustedOrderMatches = false
				state.add("resolved_order_mismatch", step.ID, "arguments.order_id", "step order must exactly match the trusted resolved order")
			}
		}
	}

	if !orderBound {
		state.validateOrderlessAmounts()
		return
	}
	if !safeOrderIDPattern.MatchString(state.context.ResolvedOrderID) {
		state.add("invalid_context", "", "resolved_order_id", "a safe trusted resolved order id is required for order-bound plans")
		return
	}
	if !trustedOrderMatches {
		return
	}

	var fact factResult
	if verifier == nil || verifier.facts == nil {
		fact.err = errors.New("fact reader unavailable")
	} else {
		fact.order, fact.err = verifier.facts.GetOrder(ctx, state.context.UserID, state.context.ResolvedOrderID)
	}
	for index, step := range state.plan.Steps {
		arguments := state.parsedArguments[index]
		if arguments == nil {
			continue
		}
		orderID, hasOrder := stringArgument(arguments, "order_id")
		if hasOrder {
			if fact.err != nil || fact.order.ID != state.context.ResolvedOrderID || orderID != fact.order.ID {
				state.add("fact_unavailable", step.ID, "arguments.order_id", "current order facts are unavailable")
			} else if fact.order.UserID != state.context.UserID {
				state.add("order_not_owned", step.ID, "arguments.order_id", "order does not belong to the verified user")
			}
		}

		switch step.Tool {
		case "issue_refund":
			amount, hasAmount := int64Argument(arguments, "amount_cents")
			if hasAmount && hasOrder && fact.err == nil && fact.order.ID == state.context.ResolvedOrderID && fact.order.UserID == state.context.UserID {
				balance := fact.order.PaidAmountCents - fact.order.RefundedAmountCents
				if amount <= 0 || amount > balance {
					state.add("refund_amount_exceeds_balance", step.ID, "arguments.amount_cents", "refund must be positive and no greater than the current refundable balance")
				}
			}
		case "create_replacement":
			sku, hasSKU := stringArgument(arguments, "sku")
			if hasSKU && fact.err == nil && fact.order.ID == state.context.ResolvedOrderID && fact.order.UserID == state.context.UserID && sku != fact.order.SKU {
				state.add("sku_mismatch", step.ID, "arguments.sku", "replacement sku must exactly match the trusted order sku")
			}
		}
	}
	state.validateOrderlessAmounts()
}

func (state *verificationState) validateOrderlessAmounts() {
	for index, step := range state.plan.Steps {
		if step.Tool != "issue_coupon" || state.parsedArguments[index] == nil {
			continue
		}
		amount, hasAmount := int64Argument(state.parsedArguments[index], "amount_cents")
		if !hasAmount {
			continue
		}
		capCents, hasCap := state.couponCap()
		if !hasCap {
			state.add("coupon_cap_missing", step.ID, "policy_refs", "applicable customer-care evidence must provide a typed coupon cap")
		} else if amount <= 0 || amount > capCents {
			state.add("coupon_amount_exceeds_cap", step.ID, "arguments.amount_cents", "coupon must be positive and no greater than the typed policy cap")
		}
	}
}

func (state *verificationState) couponCap() (int64, bool) {
	evidence := state.applicableEvidence["customer_care"]
	if len(evidence) == 0 {
		return 0, false
	}
	var capCents int64
	for _, item := range evidence {
		if item.MaxCouponCents == nil || *item.MaxCouponCents <= 0 {
			return 0, false
		}
		if capCents == 0 || *item.MaxCouponCents < capCents {
			capCents = *item.MaxCouponCents
		}
	}
	return capCents, capCents > 0
}

func (state *verificationState) add(code, stepID, field, message string) {
	candidate := Error{Code: code, StepID: stepID, Field: field, Message: message}
	for _, existing := range state.errors {
		if existing == candidate {
			return
		}
	}
	state.errors = append(state.errors, candidate)
}

type validatedArguments struct {
	values    map[string]any
	canonical string
}

func validateArguments(arguments, schemaJSON json.RawMessage) (validatedArguments, error) {
	if trimmed := bytes.TrimSpace(arguments); len(trimmed) == 0 || trimmed[0] != '{' {
		return validatedArguments{}, errors.New("arguments must be an object")
	}
	value, err := decodeStrictJSON(arguments)
	if err != nil {
		return validatedArguments{}, err
	}
	values, ok := value.(map[string]any)
	if !ok {
		return validatedArguments{}, errors.New("arguments must be an object")
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return validatedArguments{}, fmt.Errorf("decode registry schema: %w", err)
	}
	normalized, err := normalizeForSchema(values, &schema)
	if err != nil {
		return validatedArguments{}, err
	}
	normalizedValues, ok := normalized.(map[string]any)
	if !ok {
		return validatedArguments{}, errors.New("normalized arguments must be an object")
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return validatedArguments{}, fmt.Errorf("resolve registry schema: %w", err)
	}
	if err := resolved.Validate(normalizedValues); err != nil {
		return validatedArguments{}, err
	}
	canonical, err := json.Marshal(normalizedValues)
	if err != nil {
		return validatedArguments{}, err
	}
	return validatedArguments{values: normalizedValues, canonical: string(canonical)}, nil
}

func decodeStrictJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeStrictValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeStrictValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("object key must be a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate object key %q", key)
			}
			value, err := decodeStrictValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeStrictValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func normalizeForSchema(value any, schema *jsonschema.Schema) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		return normalizeJSONNumber(typed, schema)
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, child := range typed {
			childSchema := schema.Properties[key]
			if childSchema == nil {
				childSchema = &jsonschema.Schema{}
			}
			value, err := normalizeForSchema(child, childSchema)
			if err != nil {
				return nil, err
			}
			normalized[key] = value
		}
		return normalized, nil
	case []any:
		normalized := make([]any, len(typed))
		itemSchema := schema.Items
		if itemSchema == nil {
			itemSchema = &jsonschema.Schema{}
		}
		for index, child := range typed {
			value, err := normalizeForSchema(child, itemSchema)
			if err != nil {
				return nil, err
			}
			normalized[index] = value
		}
		return normalized, nil
	default:
		return value, nil
	}
}

func normalizeJSONNumber(number json.Number, schema *jsonschema.Schema) (any, error) {
	if schemaAcceptsType(schema, "integer") {
		value, ok := new(big.Rat).SetString(number.String())
		if !ok || value.Denom().Cmp(big.NewInt(1)) != 0 || !value.Num().IsInt64() {
			return nil, errors.New("number is not an exact int64")
		}
		return value.Num().Int64(), nil
	}
	value, err := number.Float64()
	if err != nil {
		return nil, err
	}
	return value, nil
}

func schemaAcceptsType(schema *jsonschema.Schema, want string) bool {
	if schema.Type == want {
		return true
	}
	for _, candidate := range schema.Types {
		if candidate == want {
			return true
		}
	}
	return false
}

func stringArgument(arguments map[string]any, name string) (string, bool) {
	value, exists := arguments[name]
	if !exists {
		return "", false
	}
	decoded, ok := value.(string)
	return decoded, ok
}

func int64Argument(arguments map[string]any, name string) (int64, bool) {
	value, exists := arguments[name]
	if !exists {
		return 0, false
	}
	decoded, ok := value.(int64)
	return decoded, ok
}

func canonicalScopes(scopes []string) []string {
	canonical := append([]string{}, scopes...)
	sort.Strings(canonical)
	result := canonical[:0]
	for _, scope := range canonical {
		if len(result) == 0 || result[len(result)-1] != scope {
			result = append(result, scope)
		}
	}
	return result
}

func clonePlan(plan agent.ActionPlan) agent.ActionPlan {
	clone := agent.ActionPlan{
		Goal: plan.Goal, PolicyRefs: append([]string{}, plan.PolicyRefs...), Steps: make([]agent.Step, len(plan.Steps)),
	}
	for index, step := range plan.Steps {
		clone.Steps[index] = step
		clone.Steps[index].Arguments = append(json.RawMessage(nil), step.Arguments...)
		clone.Steps[index].DependsOn = append([]string{}, step.DependsOn...)
	}
	return clone
}

type policyRequirement struct {
	risk     toolkit.Risk
	region   string
	category string
}

var policyRequirements = map[string]policyRequirement{
	"damaged_goods": {risk: toolkit.Write, region: supportedRegion, category: supportedCategory},
	"refunds":       {risk: toolkit.HighRiskWrite, region: supportedRegion, category: supportedCategory},
	"customer_care": {risk: toolkit.HighRiskWrite, region: supportedRegion, category: supportedCategory},
}

func evidenceApplicable(evidence policy.Evidence, now time.Time, requirement policyRequirement) bool {
	now = now.UTC()
	return evidence.RiskLevel == requirement.risk &&
		evidence.Region == requirement.region && evidence.ProductCategory == requirement.category &&
		!now.Before(evidence.EffectiveFrom.UTC()) && now.Before(evidence.EffectiveTo.UTC())
}

var requiredPolicyByTool = map[string]string{
	"create_return":      "damaged_goods",
	"create_replacement": "damaged_goods",
	"issue_refund":       "refunds",
	"issue_coupon":       "customer_care",
}

var successConditions = map[string]map[string]struct{}{
	"get_order":          stringSet("order.exists", "order.status == 'delivered'"),
	"get_shipment":       stringSet("shipment.exists", "shipment.status == 'delivered'"),
	"check_inventory":    stringSet("inventory.available", "inventory.available > 0"),
	"check_eligibility":  stringSet("eligibility.eligible", "eligibility.eligible == true"),
	"create_return":      stringSet("return.created", "return.status == 'created'"),
	"create_replacement": stringSet("replacement.created", "replacement.status == 'created'"),
	"issue_refund":       stringSet("refund.created", "refund.status == 'created'"),
	"issue_coupon":       stringSet("coupon.created", "coupon.status == 'created'"),
}

func validSuccessCondition(tool, condition string) bool {
	_, valid := successConditions[tool][condition]
	return valid
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

type graphRules struct {
	allowedDependencies  map[string]map[string]struct{}
	requiredDependencies map[string][]string
}

var prerequisiteRules = map[string]graphRules{
	"get_order": graph("get_order", nil, nil),
	"get_shipment": graph("get_shipment", map[string][]string{
		"get_order": {}, "get_shipment": {"get_order"},
	}, nil),
	"check_inventory": graph("check_inventory", nil, nil),
	"check_eligibility": graph("check_eligibility", map[string][]string{
		"get_order": {}, "check_eligibility": {"get_order"},
	}, map[string][]string{"check_eligibility": {"get_order"}}),
	"create_return": graph("create_return", map[string][]string{
		"get_order": {}, "check_eligibility": {"get_order"}, "create_return": {"check_eligibility"},
	}, map[string][]string{"check_eligibility": {"get_order"}, "create_return": {"check_eligibility"}}),
	"create_replacement": graph("create_replacement", map[string][]string{
		"get_order": {}, "get_shipment": {"get_order"}, "check_eligibility": {"get_order"}, "check_inventory": {}, "create_replacement": {"get_shipment", "check_eligibility", "check_inventory"},
	}, map[string][]string{"get_shipment": {"get_order"}, "check_eligibility": {"get_order"}, "create_replacement": {"get_shipment", "check_eligibility", "check_inventory"}}),
	"issue_refund": graph("issue_refund", map[string][]string{
		"get_order": {}, "issue_refund": {"get_order"},
	}, map[string][]string{"issue_refund": {"get_order"}}),
	"issue_coupon": graph("issue_coupon", map[string][]string{
		"get_order": {}, "get_shipment": {"get_order"}, "check_eligibility": {"get_order"}, "check_inventory": {}, "create_replacement": {"get_shipment", "check_eligibility", "check_inventory"}, "issue_coupon": {"create_replacement"},
	}, map[string][]string{"get_shipment": {"get_order"}, "check_eligibility": {"get_order"}, "create_replacement": {"get_shipment", "check_eligibility", "check_inventory"}}),
}

func graph(terminal string, allowed, required map[string][]string) graphRules {
	if allowed == nil {
		allowed = map[string][]string{terminal: {}}
	}
	rules := graphRules{
		allowedDependencies:  make(map[string]map[string]struct{}, len(allowed)),
		requiredDependencies: required,
	}
	for tool, dependencies := range allowed {
		rules.allowedDependencies[tool] = stringSet(dependencies...)
	}
	return rules
}

func dependencyCycle(steps []agent.Step, byID map[string]int) string {
	const (
		unvisited = iota
		visiting
		visited
	)
	states := make([]int, len(steps))
	var visit func(int) string
	visit = func(index int) string {
		if states[index] == visiting {
			return steps[index].ID
		}
		if states[index] == visited {
			return ""
		}
		states[index] = visiting
		for _, dependency := range steps[index].DependsOn {
			dependencyIndex, exists := byID[dependency]
			if exists {
				if cycle := visit(dependencyIndex); cycle != "" {
					return cycle
				}
			}
		}
		states[index] = visited
		return ""
	}
	for index := range steps {
		if cycle := visit(index); cycle != "" {
			return cycle
		}
	}
	return ""
}

func collectAncestors(stepID string, steps []agent.Step, byID map[string]int, ancestors map[string]struct{}) {
	if _, exists := ancestors[stepID]; exists {
		return
	}
	ancestors[stepID] = struct{}{}
	stepIndex, exists := byID[stepID]
	if !exists {
		return
	}
	for _, dependency := range steps[stepIndex].DependsOn {
		collectAncestors(dependency, steps, byID, ancestors)
	}
}
