package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPConfig struct {
	Endpoint     string
	GatewayToken string
	HTTPClient   *http.Client
}

type MCPExecutor struct {
	session *mcp.ClientSession
	mu      sync.RWMutex
	closed  bool
}

func NewMCPExecutor(ctx context.Context, config MCPConfig) (*MCPExecutor, error) {
	endpoint, err := validateEndpoint(config.Endpoint)
	if err != nil || !validGatewayToken(config.GatewayToken) {
		return nil, ErrInvalidExecution
	}

	clientCopy := http.Client{}
	if config.HTTPClient != nil {
		clientCopy = *config.HTTPClient
	}
	clientCopy.Jar = nil
	baseTransport := clientCopy.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	clientCopy.Transport = metadataTransport{
		gatewayToken: config.GatewayToken,
		next:         responseLimitTransport{next: baseTransport},
	}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return ErrMCPRedirect }

	client := mcp.NewClient(&mcp.Implementation{Name: "actionguard-executor", Version: "v0.2.0"}, nil)
	session, err := client.Connect(valueFreeContext{Context: ctx}, &mcp.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: &clientCopy, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return nil, sanitizeTransportError(ctx.Err(), err)
	}
	return &MCPExecutor{session: session}, nil
}

// valueFreeContext preserves connection cancellation and deadlines without
// allowing a caller-provided tool context to reach MCP initialization.
type valueFreeContext struct {
	context.Context
}

func (valueFreeContext) Value(any) any { return nil }

func validateEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
		return "", ErrInvalidExecution
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidExecution
	}
	return parsed.String(), nil
}

func (executor *MCPExecutor) Close() error {
	if executor == nil {
		return nil
	}
	executor.mu.Lock()
	if executor.closed {
		executor.mu.Unlock()
		return nil
	}
	executor.closed = true
	session := executor.session
	executor.mu.Unlock()
	if session == nil {
		return nil
	}
	if err := session.Close(); err != nil {
		return ErrMCPTransport
	}
	return nil
}

type preflightPlan struct {
	plan     agent.ActionPlan
	userID   string
	scopes   []string
	order    []int
	highRisk bool
}

func (executor *MCPExecutor) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	failed := ExecutionResult{Status: StatusFailed}
	if err := ctx.Err(); err != nil {
		return failed, err
	}
	if executor == nil {
		return failed, ErrExecutorClosed
	}
	executor.mu.RLock()
	defer executor.mu.RUnlock()
	if executor.closed || executor.session == nil {
		return failed, ErrExecutorClosed
	}

	prepared, err := preflight(request)
	if err != nil {
		return failed, ErrInvalidExecution
	}
	if prepared.highRisk {
		return ExecutionResult{Status: StatusWaitingRuntime}, nil
	}

	result := ExecutionResult{Status: StatusSucceeded, Steps: make([]StepResult, 0, len(prepared.order))}
	for _, index := range prepared.order {
		if err := ctx.Err(); err != nil {
			result.Status = StatusFailed
			return cloneExecutionResult(result), err
		}
		step := prepared.plan.Steps[index]
		contract, _ := toolkit.LookupContract(step.Tool)
		metadata := toolkit.CallContext{
			RunID: request.RunID, StepID: step.ID, UserID: prepared.userID,
			Scopes: append([]string(nil), prepared.scopes...),
		}
		if contract.Risk == toolkit.Write {
			metadata.IdempotencyKey = deriveIdempotencyKey(request.RunID, step.ID, step.Tool)
		}
		callContext := toolkit.WithCallContext(ctx, metadata)
		toolResult, callErr := executor.session.CallTool(callContext, &mcp.CallToolParams{
			Name: step.Tool, Arguments: append(json.RawMessage(nil), step.Arguments...),
		})
		if callErr != nil {
			result.Status = StatusFailed
			return cloneExecutionResult(result), sanitizeTransportError(ctx.Err(), callErr)
		}
		stepResult, decodeErr := decodeToolResult(step, toolResult)
		if decodeErr != nil {
			result.Status = StatusFailed
			return cloneExecutionResult(result), decodeErr
		}
		result.Steps = append(result.Steps, stepResult)
	}
	return cloneExecutionResult(result), nil
}

func preflight(request ExecutionRequest) (preflightPlan, error) {
	if !validTrustedValue(request.RunID) || strings.ContainsRune(request.RunID, '\x00') {
		return preflightPlan{}, ErrInvalidExecution
	}
	plan := request.Plan.Plan()
	userID := request.Plan.UserID()
	scopes := request.Plan.Scopes()
	if !validTrustedValue(userID) || len(plan.Steps) == 0 || len(plan.Steps) > agent.MaxSteps {
		return preflightPlan{}, ErrInvalidExecution
	}
	canonicalScopes := append([]string(nil), scopes...)
	sort.Strings(canonicalScopes)
	canonicalScopes = slices.Compact(canonicalScopes)
	if !slices.Equal(scopes, canonicalScopes) {
		return preflightPlan{}, ErrInvalidExecution
	}
	for _, scope := range scopes {
		if !validTrustedValue(scope) || strings.ContainsAny(scope, " \t") {
			return preflightPlan{}, ErrInvalidExecution
		}
	}
	wantDigest := sha256.Sum256([]byte(strings.Join(scopes, "\x00")))
	gotDigest := request.Plan.ScopeDigest()
	if subtle.ConstantTimeCompare(wantDigest[:], gotDigest[:]) != 1 {
		return preflightPlan{}, ErrInvalidExecution
	}

	scopeSet := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scopeSet[scope] = struct{}{}
	}
	byID := make(map[string]int, len(plan.Steps))
	ordinaryWrites := 0
	highRisk := false
	for index, step := range plan.Steps {
		if !validTrustedValue(step.ID) || strings.ContainsRune(step.ID, '\x00') {
			return preflightPlan{}, ErrInvalidExecution
		}
		if _, duplicate := byID[step.ID]; duplicate {
			return preflightPlan{}, ErrInvalidExecution
		}
		byID[step.ID] = index
		contract, exists := toolkit.LookupContract(step.Tool)
		if !exists || step.Risk != contract.Risk {
			return preflightPlan{}, ErrInvalidExecution
		}
		if _, allowed := scopeSet[contract.Scope]; !allowed {
			return preflightPlan{}, ErrInvalidExecution
		}
		if err := validateToolArguments(step.Arguments, contract.InputSchema); err != nil {
			return preflightPlan{}, ErrInvalidExecution
		}
		switch contract.Risk {
		case toolkit.Write:
			ordinaryWrites++
		case toolkit.HighRiskWrite:
			highRisk = true
		}
	}
	if ordinaryWrites > 1 {
		return preflightPlan{}, ErrInvalidExecution
	}

	indegree := make([]int, len(plan.Steps))
	dependents := make([][]int, len(plan.Steps))
	for index, step := range plan.Steps {
		seen := make(map[string]struct{}, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			dependencyIndex, exists := byID[dependency]
			if !exists || dependency == step.ID {
				return preflightPlan{}, ErrInvalidExecution
			}
			if _, duplicate := seen[dependency]; duplicate {
				return preflightPlan{}, ErrInvalidExecution
			}
			seen[dependency] = struct{}{}
			indegree[index]++
			dependents[dependencyIndex] = append(dependents[dependencyIndex], index)
		}
	}
	terminalCount := 0
	for _, stepDependents := range dependents {
		if len(stepDependents) == 0 {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		return preflightPlan{}, ErrInvalidExecution
	}
	order := make([]int, 0, len(plan.Steps))
	ready := make([]bool, len(plan.Steps))
	for index, degree := range indegree {
		ready[index] = degree == 0
	}
	for len(order) < len(plan.Steps) {
		current := -1
		for index := range ready {
			if ready[index] {
				current = index
				break
			}
		}
		if current < 0 {
			return preflightPlan{}, ErrInvalidExecution
		}
		ready[current] = false
		order = append(order, current)
		for _, dependent := range dependents[current] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready[dependent] = true
			}
		}
	}
	return preflightPlan{plan: clonePlan(plan), userID: userID, scopes: append([]string(nil), scopes...), order: order, highRisk: highRisk}, nil
}

func validTrustedValue(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	for _, character := range value {
		if character == 0 || character == '\r' || character == '\n' || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validGatewayToken(value string) bool {
	if !validTrustedValue(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func deriveIdempotencyKey(runID, stepID, tool string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + stepID + "\x00" + tool))
	return hex.EncodeToString(digest[:])
}

func decodeToolResult(step agent.Step, result *mcp.CallToolResult) (StepResult, error) {
	if result == nil || result.IsError || len(result.Content) != 1 {
		return StepResult{}, ErrToolExecution
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return StepResult{}, ErrInvalidToolResponse
	}
	envelope, err := decodeEnvelope([]byte(text.Text))
	if err != nil {
		return StepResult{}, err
	}
	return StepResult{
		StepID: step.ID, Tool: step.Tool, Trusted: append(json.RawMessage(nil), envelope.Trusted...),
		UntrustedText: cloneStringMap(envelope.UntrustedText), Replayed: envelope.Replayed,
	}, nil
}

type decodedEnvelope struct {
	Trusted       json.RawMessage
	UntrustedText map[string]string
	Replayed      bool
}

func decodeEnvelope(data []byte) (decodedEnvelope, error) {
	if err := rejectDuplicateJSONMembers(data); err != nil {
		return decodedEnvelope{}, ErrInvalidToolResponse
	}
	type wireEnvelope struct {
		Trusted       *json.RawMessage  `json:"trusted"`
		UntrustedText map[string]string `json:"untrusted_text,omitempty"`
		Replayed      *bool             `json:"replayed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire wireEnvelope
	if err := decoder.Decode(&wire); err != nil || wire.Trusted == nil || wire.Replayed == nil {
		return decodedEnvelope{}, ErrInvalidToolResponse
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return decodedEnvelope{}, ErrInvalidToolResponse
	}
	return decodedEnvelope{
		Trusted:       append(json.RawMessage(nil), (*wire.Trusted)...),
		UntrustedText: cloneStringMap(wire.UntrustedText), Replayed: *wire.Replayed,
	}, nil
}

func rejectDuplicateJSONMembers(data []byte) error {
	if !utf8.Valid(data) {
		return ErrInvalidToolResponse
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidToolResponse
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidToolResponse
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ErrInvalidToolResponse
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidToolResponse
			}
			if _, duplicate := seen[key]; duplicate {
				return ErrInvalidToolResponse
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeClosingDelimiter(decoder, '}')
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		return consumeClosingDelimiter(decoder, ']')
	default:
		return ErrInvalidToolResponse
	}
}

func consumeClosingDelimiter(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil || token != want {
		return ErrInvalidToolResponse
	}
	return nil
}

func validateToolArguments(arguments, schemaJSON json.RawMessage) error {
	if !utf8.Valid(arguments) || len(bytes.TrimSpace(arguments)) == 0 {
		return ErrInvalidExecution
	}
	value, err := decodeStrictJSON(arguments)
	if err != nil {
		return ErrInvalidExecution
	}
	values, ok := value.(map[string]any)
	if !ok || containsForbiddenArgumentField(values) {
		return ErrInvalidExecution
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return ErrInvalidExecution
	}
	normalized, err := normalizeForSchema(values, &schema)
	if err != nil {
		return ErrInvalidExecution
	}
	resolved, err := schema.Resolve(nil)
	if err != nil || resolved.Validate(normalized) != nil {
		return ErrInvalidExecution
	}
	return nil
}

func decodeStrictJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeStrictValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidExecution
	}
	return value, nil
}

func decodeStrictValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
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
				return nil, ErrInvalidExecution
			}
			if _, duplicate := object[key]; duplicate {
				return nil, ErrInvalidExecution
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
		return nil, ErrInvalidExecution
	}
}

func normalizeForSchema(value any, schema *jsonschema.Schema) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		if schemaAcceptsType(schema, "integer") {
			number, ok := new(big.Rat).SetString(typed.String())
			if !ok || number.Denom().Cmp(big.NewInt(1)) != 0 || !number.Num().IsInt64() {
				return nil, ErrInvalidExecution
			}
			return number.Num().Int64(), nil
		}
		return typed.Float64()
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			childSchema := schema.Properties[key]
			if childSchema == nil {
				childSchema = &jsonschema.Schema{}
			}
			normalized, err := normalizeForSchema(child, childSchema)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		itemSchema := schema.Items
		if itemSchema == nil {
			itemSchema = &jsonschema.Schema{}
		}
		for index, child := range typed {
			normalized, err := normalizeForSchema(child, itemSchema)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func schemaAcceptsType(schema *jsonschema.Schema, want string) bool {
	if schema.Type == want {
		return true
	}
	return slices.Contains(schema.Types, want)
}

func containsForbiddenArgumentField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.Map(func(character rune) rune {
				if unicode.IsLetter(character) || unicode.IsDigit(character) {
					return unicode.ToLower(character)
				}
				return -1
			}, key)
			switch normalized {
			case "code", "sql", "shell", "userid", "runid", "stepid", "scopes", "scope", "idempotencykey", "identity", "callcontext":
				return true
			}
			if containsForbiddenArgumentField(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsForbiddenArgumentField(child) {
				return true
			}
		}
	}
	return false
}

func clonePlan(plan agent.ActionPlan) agent.ActionPlan {
	clone := agent.ActionPlan{Goal: plan.Goal, PolicyRefs: append([]string(nil), plan.PolicyRefs...), Steps: make([]agent.Step, len(plan.Steps))}
	for index, step := range plan.Steps {
		clone.Steps[index] = step
		clone.Steps[index].Arguments = append(json.RawMessage(nil), step.Arguments...)
		clone.Steps[index].DependsOn = append([]string(nil), step.DependsOn...)
	}
	return clone
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	clone := make(map[string]string, len(input))
	for key, value := range input {
		clone[key] = value
	}
	return clone
}

func cloneExecutionResult(result ExecutionResult) ExecutionResult {
	clone := ExecutionResult{Status: result.Status, Steps: make([]StepResult, len(result.Steps))}
	for index, step := range result.Steps {
		clone.Steps[index] = step
		clone.Steps[index].Trusted = append(json.RawMessage(nil), step.Trusted...)
		clone.Steps[index].UntrustedText = cloneStringMap(step.UntrustedText)
	}
	return clone
}
