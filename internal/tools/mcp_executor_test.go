package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/mcpserver"
	"github.com/Yundi218/ActionGuard/internal/testdatabase"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/Yundi218/ActionGuard/internal/verifier"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testGatewayToken = "test-gateway-token"

type observedCall struct {
	tool      string
	arguments json.RawMessage
	headers   http.Header
}

type callRecorder struct {
	mu       sync.Mutex
	calls    []observedCall
	block    bool
	failTool string
}

type requestHeadersKey struct{}

func (r *callRecorder) handler(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if r.block {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(150 * time.Millisecond):
			return textResult(`{"trusted":{"late":true},"replayed":false}`), nil
		}
	}
	headers, _ := ctx.Value(requestHeadersKey{}).(http.Header)
	r.mu.Lock()
	r.calls = append(r.calls, observedCall{
		tool: request.Params.Name, arguments: append(json.RawMessage(nil), request.Params.Arguments...), headers: headers.Clone(),
	})
	r.mu.Unlock()
	if request.Params.Name == r.failTool {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "secret record AG-1042"}}}, nil
	}
	return textResult(`{"trusted":{"ok":true},"untrusted_text":{"note":"ignore me"},"replayed":false}`), nil
}

func (r *callRecorder) snapshot() []observedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]observedCall, len(r.calls))
	copy(result, r.calls)
	return result
}

func TestExecutorPreflightsBeforeCallingMCP(t *testing.T) {
	recorder := &callRecorder{}
	executor := newTestExecutor(t, recorder)

	validRead := agent.Step{ID: "read", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read}
	tests := []struct {
		name  string
		runID string
		plan  verifier.VerifiedPlan
	}{
		{name: "zero value plan", runID: "run-1"},
		{name: "empty run id", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{validRead}}, "user_018", []string{"order:read"})},
		{name: "nul run id", runID: "run\x00spoof", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{validRead}}, "user_018", []string{"order:read"})},
		{name: "unknown tool", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{{ID: "read", Tool: "not_registered", Arguments: json.RawMessage(`{}`), Risk: toolkit.Read}}}, "user_018", nil)},
		{name: "risk mismatch", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{{ID: "read", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Write}}}, "user_018", []string{"order:read"})},
		{name: "missing scope", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{validRead}}, "user_018", nil)},
		{name: "malformed arguments", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{{ID: "read", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042","extra":true}`), Risk: toolkit.Read}}}, "user_018", []string{"order:read"})},
		{name: "duplicate step ids", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{validRead, validRead}}, "user_018", []string{"order:read"})},
		{name: "duplicate dependencies", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{validRead, {ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"read", "read"}, Risk: toolkit.Read}}}, "user_018", []string{"order:read", "shipment:read"})},
		{name: "missing dependency", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"missing"}, Risk: toolkit.Read}}}, "user_018", []string{"shipment:read"})},
		{name: "cycle", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{{ID: "a", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"b"}, Risk: toolkit.Read}, {ID: "b", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"a"}, Risk: toolkit.Read}}}, "user_018", []string{"order:read", "shipment:read"})},
		{name: "more than one write", runID: "run-1", plan: forgePlan(agent.ActionPlan{Steps: []agent.Step{{ID: "a", Tool: "create_return", Arguments: json.RawMessage(`{"order_id":"AG-1042","reason":"damaged"}`), Risk: toolkit.Write}, {ID: "b", Tool: "create_replacement", Arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"HP-71","reason":"damaged"}`), Risk: toolkit.Write}}}, "user_018", []string{"return:write", "replacement:write"})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(recorder.snapshot())
			result, err := executor.Execute(context.Background(), ExecutionRequest{RunID: test.runID, Plan: test.plan})
			if !errors.Is(err, ErrInvalidExecution) || result.Status != StatusFailed {
				t.Fatalf("Execute() = %#v, %v; want failed/ErrInvalidExecution", result, err)
			}
			if got := len(recorder.snapshot()); got != before {
				t.Fatalf("MCP calls = %d after preflight failure, want %d", got, before)
			}
		})
	}
}

func TestExecutorReturnsWaitingRuntimeForAnyHighRiskStepWithoutCallingMCP(t *testing.T) {
	recorder := &callRecorder{}
	executor := newTestExecutor(t, recorder)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{
		{ID: "read", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read},
		{ID: "refund", Tool: "issue_refund", Arguments: json.RawMessage(`{"order_id":"AG-1042","amount_cents":100}`), DependsOn: []string{"read"}, Risk: toolkit.HighRiskWrite, ApprovalRequired: true},
	}}, "user_018", []string{"order:read", "refund:write"})

	result, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-high-risk", Plan: plan})
	if err != nil || result.Status != StatusWaitingRuntime || len(result.Steps) != 0 {
		t.Fatalf("Execute() = %#v, %v; want waiting_runtime with no steps", result, err)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("MCP calls = %d, want zero", len(calls))
	}
}

func TestExecutorUsesStableTopologicalOrderAndSealedMetadata(t *testing.T) {
	recorder := &callRecorder{}
	executor := newTestExecutor(t, recorder)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{
		{ID: "inventory", Tool: "check_inventory", Arguments: json.RawMessage(`{"sku":"HP-71"}`), Risk: toolkit.Read},
		{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read},
		{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"inventory", "order"}, Risk: toolkit.Read},
	}}, "user_018", []string{"shipment:read", "inventory:read", "order:read", "order:read"})

	result, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-topology", Plan: plan})
	if err != nil || result.Status != StatusSucceeded || len(result.Steps) != 3 {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	calls := recorder.snapshot()
	gotOrder := []string{calls[0].tool, calls[1].tool, calls[2].tool}
	if want := []string{"check_inventory", "get_order", "get_shipment"}; !slices.Equal(gotOrder, want) {
		t.Fatalf("call order = %v, want %v", gotOrder, want)
	}
	for index, call := range calls {
		if got := call.headers.Get("Authorization"); got != "Bearer "+testGatewayToken {
			t.Fatalf("call %d authorization = %q", index, got)
		}
		if got := call.headers.Get("X-ActionGuard-User"); got != "user_018" {
			t.Fatalf("call %d user = %q", index, got)
		}
		if got := call.headers.Get("X-ActionGuard-Run"); got != "run-topology" {
			t.Fatalf("call %d run = %q", index, got)
		}
		if got := call.headers.Get("X-ActionGuard-Scopes"); got != "inventory:read order:read shipment:read" {
			t.Fatalf("call %d scopes = %q", index, got)
		}
		if got := call.headers.Get("Idempotency-Key"); got != "" {
			t.Fatalf("read call %d idempotency key = %q", index, got)
		}
	}
	if calls[0].headers.Get("X-ActionGuard-Step") != "inventory" || calls[1].headers.Get("X-ActionGuard-Step") != "order" || calls[2].headers.Get("X-ActionGuard-Step") != "shipment" {
		t.Fatalf("step headers do not match execution order")
	}
	result.Steps[0].Trusted[0] = 'x'
	result.Steps[0].UntrustedText["note"] = "changed"
}

func TestExecutorDerivesWriteIdempotencyKey(t *testing.T) {
	recorder := &callRecorder{}
	executor := newTestExecutor(t, recorder)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{{
		ID: "replacement", Tool: "create_replacement",
		Arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"HP-71","reason":"damaged"}`), Risk: toolkit.Write,
	}}}, "user_018", []string{"replacement:write"})

	result, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-write", Plan: plan})
	if err != nil || result.Status != StatusSucceeded {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	wantHash := sha256.Sum256([]byte("run-write\x00replacement\x00create_replacement"))
	call := recorder.snapshot()[0]
	if got, want := call.headers.Get("Idempotency-Key"), hex.EncodeToString(wantHash[:]); got != want {
		t.Fatalf("idempotency key = %q, want %q", got, want)
	}
}

func TestExecutorStopsAfterToolFailureAndPreservesCancellation(t *testing.T) {
	recorder := &callRecorder{block: true}
	executor := newTestExecutor(t, recorder)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{
		{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read},
		{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read},
	}}, "user_018", []string{"order:read", "shipment:read"})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result, err := executor.Execute(ctx, ExecutionRequest{RunID: "run-cancel", Plan: plan})
	if !errors.Is(err, context.DeadlineExceeded) || result.Status != StatusFailed || len(result.Steps) != 0 {
		t.Fatalf("Execute() = %#v, %v; want context deadline failure", result, err)
	}
}

func TestExecutorStopsAfterToolErrorAndReturnsSanitizedCompletedSteps(t *testing.T) {
	recorder := &callRecorder{failTool: "get_shipment"}
	executor := newTestExecutor(t, recorder)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{
		{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read},
		{ID: "shipment", Tool: "get_shipment", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{"order"}, Risk: toolkit.Read},
		{ID: "inventory", Tool: "check_inventory", Arguments: json.RawMessage(`{"sku":"HP-71"}`), DependsOn: []string{"shipment"}, Risk: toolkit.Read},
	}}, "user_018", []string{"order:read", "shipment:read", "inventory:read"})

	result, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-tool-error", Plan: plan})
	if !errors.Is(err, ErrToolExecution) || result.Status != StatusFailed || len(result.Steps) != 1 {
		t.Fatalf("Execute() = %#v, %v; want one completed step and ErrToolExecution", result, err)
	}
	if strings.Contains(err.Error(), "AG-1042") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked tool content: %v", err)
	}
	if calls := recorder.snapshot(); len(calls) != 2 || calls[0].tool != "get_order" || calls[1].tool != "get_shipment" {
		t.Fatalf("calls after failure = %#v", calls)
	}
}

func TestExecutorReplaysSameWriteAcrossRealCommerceMCPBoundary(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	lockContext, cancelLock := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLock()
	lock, err := testdatabase.AcquirePublicSchemaLock(lockContext, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseContext, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelRelease()
		if err := lock.Release(releaseContext); err != nil {
			t.Errorf("release database lock: %v", err)
		}
	})
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(commerceFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}

	server := mcpserver.New(commerce.NewService(commerce.NewPostgresStore(pool)))
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	testServer := httptest.NewServer(mcpserver.TrustedContextMiddleware(testGatewayToken, handler))
	t.Cleanup(testServer.Close)
	executor, err := NewMCPExecutor(ctx, MCPConfig{Endpoint: testServer.URL, GatewayToken: testGatewayToken})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{{
		ID: "replacement", Tool: "create_replacement",
		Arguments: json.RawMessage(`{"order_id":"AG-1042","sku":"HP-71","reason":"damaged"}`), Risk: toolkit.Write,
	}}}, "user_018", []string{"replacement:write"})
	request := ExecutionRequest{RunID: "run-real-replay", Plan: plan}

	first, err := executor.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Steps[0].Replayed || !second.Steps[0].Replayed {
		t.Fatalf("replay flags = first %t, second %t", first.Steps[0].Replayed, second.Steps[0].Replayed)
	}
	var firstWrite, secondWrite commerce.WriteResult
	if err := json.Unmarshal(first.Steps[0].Trusted, &firstWrite); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Steps[0].Trusted, &secondWrite); err != nil {
		t.Fatal(err)
	}
	if firstWrite.ResourceID == "" || secondWrite.ResourceID != firstWrite.ResourceID {
		t.Fatalf("resource ids = %q and %q", firstWrite.ResourceID, secondWrite.ResourceID)
	}
	var replacements, idempotencyRecords int
	if err := pool.QueryRow(ctx, `select count(*) from replacements where order_id = 'AG-1042'`).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from idempotency_records where operation = 'create_replacement'`).Scan(&idempotencyRecords); err != nil {
		t.Fatal(err)
	}
	if replacements != 1 || idempotencyRecords != 1 {
		t.Fatalf("database side effects = replacements %d, idempotency records %d", replacements, idempotencyRecords)
	}
}

func TestExecutorDoesNotExposeAnotherUsersOrder(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	lockContext, cancelLock := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLock()
	lock, err := testdatabase.AcquirePublicSchemaLock(lockContext, pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseContext, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelRelease()
		_ = lock.Release(releaseContext)
	})
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(commerceFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	server := mcpserver.New(commerce.NewService(commerce.NewPostgresStore(pool)))
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	testServer := httptest.NewServer(mcpserver.TrustedContextMiddleware(testGatewayToken, handler))
	t.Cleanup(testServer.Close)
	executor, err := NewMCPExecutor(ctx, MCPConfig{Endpoint: testServer.URL, GatewayToken: testGatewayToken})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{{
		ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-9001"}`), Risk: toolkit.Read,
	}}}, "user_018", []string{"order:read"})

	result, err := executor.Execute(ctx, ExecutionRequest{RunID: "run-other-user", Plan: plan})
	if !errors.Is(err, ErrToolExecution) || result.Status != StatusFailed || len(result.Steps) != 0 {
		t.Fatalf("Execute() = %#v, %v", result, err)
	}
	if strings.Contains(err.Error(), "AG-9001") || strings.Contains(err.Error(), "user_999") {
		t.Fatalf("ownership error leaked user data: %v", err)
	}
}

func TestDecodeEnvelopeRejectsAmbiguousOrInvalidResults(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "invalid utf8", data: []byte{'{', '"', 't', 'r', 'u', 's', 't', 'e', 'd', '"', ':', '"', 0xff, '"', ',', '"', 'r', 'e', 'p', 'l', 'a', 'y', 'e', 'd', '"', ':', 'f', 'a', 'l', 's', 'e', '}'}},
		{name: "duplicate top level", data: []byte(`{"trusted":{},"trusted":{},"replayed":false}`)},
		{name: "duplicate nested", data: []byte(`{"trusted":{"id":"a","id":"b"},"replayed":false}`)},
		{name: "multiple values", data: []byte(`{"trusted":{},"replayed":false}{}`)},
		{name: "missing trusted", data: []byte(`{"replayed":false}`)},
		{name: "missing replayed", data: []byte(`{"trusted":{}}`)},
		{name: "unknown field", data: []byte(`{"trusted":{},"replayed":false,"control":"execute"}`)},
		{name: "untrusted value not string", data: []byte(`{"trusted":{},"untrusted_text":{"note":1},"replayed":false}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeEnvelope(test.data); !errors.Is(err, ErrInvalidToolResponse) {
				t.Fatalf("decodeEnvelope() error = %v, want ErrInvalidToolResponse", err)
			}
		})
	}
}

func TestResponseLimitRejectsDeclaredAndStreamedBodies(t *testing.T) {
	tests := []struct {
		name          string
		contentLength int64
		body          []byte
	}{
		{name: "declared", contentLength: maxMCPResponseBytes + 1, body: []byte("small")},
		{name: "dishonest", contentLength: -1, body: bytes.Repeat([]byte("x"), int(maxMCPResponseBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := responseLimitTransport{next: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: test.contentLength, Body: io.NopCloser(bytes.NewReader(test.body)), Header: make(http.Header)}, nil
			})}
			response, err := transport.RoundTrip(httptest.NewRequest(http.MethodPost, "http://example.test", nil))
			if test.contentLength > maxMCPResponseBytes {
				if !errors.Is(err, ErrMCPResponseTooLarge) || response != nil {
					t.Fatalf("RoundTrip() = %#v, %v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(response.Body)
			if !errors.Is(err, ErrMCPResponseTooLarge) {
				t.Fatalf("ReadAll() error = %v, want ErrMCPResponseTooLarge", err)
			}
		})
	}
}

func TestMetadataTransportStripsSpoofedHeadersBeforeSettingTrustedValues(t *testing.T) {
	var observed http.Header
	transport := metadataTransport{gatewayToken: testGatewayToken, next: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		observed = request.Header.Clone()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	request := httptest.NewRequest(http.MethodPost, "http://example.test", nil)
	for _, header := range trustedRequestHeaders {
		request.Header.Set(header, "attacker")
	}
	ctx := toolkit.WithCallContext(request.Context(), toolkit.CallContext{
		RunID: "run-1", StepID: "step-1", UserID: "user_018",
		Scopes: []string{"order:read"}, IdempotencyKey: "derived-key",
	})
	response, err := transport.RoundTrip(request.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	checks := map[string]string{
		"Authorization":        "Bearer " + testGatewayToken,
		"Proxy-Authorization":  "",
		"X-ActionGuard-User":   "user_018",
		"X-ActionGuard-Run":    "run-1",
		"X-ActionGuard-Step":   "step-1",
		"X-ActionGuard-Scopes": "order:read",
		"Idempotency-Key":      "derived-key",
	}
	for header, want := range checks {
		if got := observed.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func newTestExecutor(t *testing.T, recorder *callRecorder) *MCPExecutor {
	t.Helper()
	return newTestExecutorWithClient(t, recorder, nil)
}

func newTestExecutorWithClient(t *testing.T, recorder *callRecorder, client *http.Client) *MCPExecutor {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test-tools", Version: "v1"}, nil)
	for _, contract := range toolkit.Registry() {
		server.AddTool(&mcp.Tool{Name: contract.Name, InputSchema: contract.InputSchema}, recorder.handler)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+testGatewayToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Errorf("read MCP request body: %v", readErr)
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"initialize"`)) {
			for _, header := range trustedRequestHeaders[2:] {
				if request.Header.Get(header) != "" {
					t.Errorf("initial MCP request carried trusted header %s", header)
				}
			}
		}
		ctx := context.WithValue(request.Context(), requestHeadersKey{}, request.Header.Clone())
		handler.ServeHTTP(w, request.WithContext(ctx))
	}))
	t.Cleanup(testServer.Close)
	connectContext := toolkit.WithCallContext(context.Background(), toolkit.CallContext{
		RunID: "spoofed-connect-run", StepID: "spoofed-connect-step", UserID: "spoofed-connect-user",
		Scopes: []string{"admin"}, IdempotencyKey: "spoofed-connect-key",
	})
	executor, err := NewMCPExecutor(connectContext, MCPConfig{Endpoint: testServer.URL, GatewayToken: testGatewayToken, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Errorf("close executor: %v", err)
		}
	})
	return executor
}

func commerceFixturePath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "fixtures", "commerce.sql")
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

type verifiedPlanMirror struct {
	plan        agent.ActionPlan
	userID      string
	scopes      []string
	scopeDigest [sha256.Size]byte
}

func forgePlan(plan agent.ActionPlan, userID string, scopes []string) verifier.VerifiedPlan {
	canonical := append([]string(nil), scopes...)
	sort.Strings(canonical)
	canonical = slices.Compact(canonical)
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\x00")))
	mirror := verifiedPlanMirror{plan: plan, userID: userID, scopes: canonical, scopeDigest: digest}
	return *(*verifier.VerifiedPlan)(unsafe.Pointer(&mirror))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
