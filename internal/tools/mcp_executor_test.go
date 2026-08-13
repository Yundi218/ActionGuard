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
	"sync/atomic"
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

func TestExecutorPreservesOversizedToolResponseClassification(t *testing.T) {
	tests := []struct {
		name string
		mode oversizedResponseMode
	}{
		{name: "declared", mode: oversizedDeclared},
		{name: "streamed", mode: oversizedStreamed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := &callRecorder{}
			transport := &oversizedToolResponseTransport{next: http.DefaultTransport, mode: test.mode}
			executor := newTestExecutorWithClientOptions(t, recorder, &http.Client{Transport: transport}, false)
			plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{{
				ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read,
			}}}, "user_018", []string{"order:read"})

			result, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-oversized-" + test.name, Plan: plan})
			if !errors.Is(err, ErrMCPResponseTooLarge) || result.Status != StatusFailed {
				t.Fatalf("Execute() = %#v, %v; want failed/ErrMCPResponseTooLarge", result, err)
			}
			_ = executor.Close()
		})
	}
}

func TestNewMCPExecutorPreservesOversizedInitializeClassificationWithoutTrustedHeaders(t *testing.T) {
	tests := []struct {
		name string
		mode oversizedResponseMode
	}{
		{name: "declared", mode: oversizedDeclared},
		{name: "streamed", mode: oversizedStreamed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "oversized-initialize", Version: "v1"}, nil)
			handler := mcp.NewStreamableHTTPHandler(
				func(*http.Request) *mcp.Server { return server },
				&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
			)
			testServer := httptest.NewServer(handler)
			t.Cleanup(testServer.Close)
			transport := &oversizedInitializeTransport{next: http.DefaultTransport, mode: test.mode}
			callerContext := toolkit.WithCallContext(context.Background(), toolkit.CallContext{
				RunID: "caller-run", StepID: "caller-step", UserID: "caller-user",
				Scopes: []string{"admin"}, IdempotencyKey: "caller-key",
			})

			executor, err := NewMCPExecutor(callerContext, MCPConfig{
				Endpoint: testServer.URL, GatewayToken: testGatewayToken,
				HTTPClient: &http.Client{Transport: transport},
			})
			if executor != nil || !errors.Is(err, ErrMCPResponseTooLarge) {
				t.Fatalf("NewMCPExecutor() = %#v, %v; want nil/ErrMCPResponseTooLarge", executor, err)
			}
			headers := transport.initializeHeaders()
			if got := headers.Get("Authorization"); got != "Bearer "+testGatewayToken {
				t.Fatalf("initialize Authorization = %q", got)
			}
			for _, header := range trustedRequestHeaders[2:] {
				if got := headers.Get(header); got != "" {
					t.Fatalf("initialize leaked trusted header %s=%q", header, got)
				}
			}
		})
	}
}

func TestNewMCPExecutorCancellationPrecedesOversizedInitialize(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "cancel-initialize", Version: "v1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	ctx, cancel := context.WithCancel(context.Background())
	transport := &oversizedInitializeTransport{next: http.DefaultTransport, mode: oversizedStreamed, cancel: cancel}

	executor, err := NewMCPExecutor(ctx, MCPConfig{
		Endpoint: testServer.URL, GatewayToken: testGatewayToken,
		HTTPClient: &http.Client{Transport: transport},
	})
	if executor != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("NewMCPExecutor() = %#v, %v; want nil/context.Canceled", executor, err)
	}
}

func TestNewMCPExecutorRejectsPreCanceledContextWithoutTransportAttempts(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	transport := &attemptCountingTransport{}
	config := MCPConfig{
		Endpoint:     "http://mcp.invalid/mcp",
		GatewayToken: testGatewayToken,
		HTTPClient:   &http.Client{Transport: transport},
	}
	for iteration := 0; iteration < 100; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		executor, err := NewMCPExecutor(ctx, config)
		if executor != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: NewMCPExecutor() = %#v, %v; want nil/context.Canceled", iteration, executor, err)
		}
	}
	if got := transport.attempts.Load(); got != 0 {
		t.Fatalf("transport attempts = %d, want zero", got)
	}

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	executor, err := NewMCPExecutor(canceledContext, MCPConfig{})
	if executor != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("NewMCPExecutor() with invalid config = %#v, %v; want cancellation precedence", executor, err)
	}
}

func TestNewMCPExecutorRejectsNilContext(t *testing.T) {
	transport := &attemptCountingTransport{}
	executor, err := NewMCPExecutor(nil, MCPConfig{
		Endpoint:     "http://mcp.invalid/mcp",
		GatewayToken: testGatewayToken,
		HTTPClient:   &http.Client{Transport: transport},
	})
	if executor != nil || !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("NewMCPExecutor() = %#v, %v; want nil/ErrInvalidExecution", executor, err)
	}
	if got := transport.attempts.Load(); got != 0 {
		t.Fatalf("transport attempts = %d, want zero", got)
	}
}

func TestConnectContextBridgeDropsCallerValuesAndReleasesResources(t *testing.T) {
	callerValueKey := &struct{ name string }{name: "caller-value"}
	deadline := time.Now().Add(time.Hour)
	caller := &trackingAfterFuncContext{
		deadline: deadline,
		done:     make(chan struct{}),
		valueKey: callerValueKey,
		value:    bytes.Repeat([]byte("s"), 1024),
		stopped:  make(chan struct{}),
	}

	bridge, cleanup := newConnectContextBridge(caller)
	if got := bridge.Value(callerValueKey); got != nil {
		t.Fatalf("bridge caller value = %#v, want nil", got)
	}
	if got, ok := bridge.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatalf("bridge deadline = %v, %t; want %v, true", got, ok, deadline)
	}

	cleanup()
	select {
	case <-caller.stopped:
	default:
		t.Fatal("bridge cleanup did not release caller cancellation callback")
	}
	select {
	case <-bridge.Done():
	default:
		t.Fatal("bridge cleanup did not cancel the bridge deadline context")
	}
}

func TestConnectContextBridgeClosesCancellationRegistrationRace(t *testing.T) {
	caller := &cancelDuringAfterFuncContext{done: make(chan struct{})}
	bridge, cleanup := newConnectContextBridge(caller)
	defer cleanup()

	select {
	case <-bridge.Done():
		if !errors.Is(bridge.Err(), context.Canceled) {
			t.Fatalf("bridge error = %v, want context.Canceled", bridge.Err())
		}
	default:
		t.Fatal("bridge returned live after caller canceled during AfterFunc registration")
	}
}

func TestNewMCPExecutorBoundsFailedInitializeCleanupWithCallerDeadline(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "bounded-initialize-cleanup", Version: "v1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	callerValueKey := &struct{ name string }{name: "failed-initialize-caller-value"}
	transport := newCleanupDeadlineTransport(http.DefaultTransport, true, oversizedStreamed, callerValueKey)
	valueContext := context.WithValue(context.Background(), callerValueKey, "must-not-reach-transport")
	deadlineContext, cancel := context.WithTimeout(valueContext, 25*time.Millisecond)
	defer cancel()
	callerContext := toolkit.WithCallContext(deadlineContext, toolkit.CallContext{
		RunID: "caller-run", StepID: "caller-step", UserID: "caller-user",
		Scopes: []string{"admin"}, IdempotencyKey: "caller-key",
	})

	started := time.Now()
	executor, err := NewMCPExecutor(callerContext, MCPConfig{
		Endpoint: testServer.URL, GatewayToken: testGatewayToken,
		HTTPClient: &http.Client{Transport: transport},
	})
	elapsed := time.Since(started)
	if executor != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NewMCPExecutor() = %#v, %v; want nil/context.DeadlineExceeded", executor, err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("NewMCPExecutor() returned after %v, want caller deadline to bound cleanup", elapsed)
	}
	transport.requireFinished(t)
	transport.requireNoTrustedHeaders(t)
	transport.requireNoCallerValues(t)
	transport.requireInitializeBodyClosed(t)
}

func TestCanceledConstructorContextDoesNotAbortBoundedNormalClose(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "bounded-normal-close", Version: "v1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	callerValueKey := &struct{ name string }{name: "successful-initialize-caller-value"}
	transport := newCleanupDeadlineTransport(http.DefaultTransport, false, oversizedDeclared, callerValueKey)
	valueContext := context.WithValue(context.Background(), callerValueKey, "must-not-reach-transport")
	constructorContext, cancelConstructor := context.WithCancel(valueContext)
	callerContext := toolkit.WithCallContext(constructorContext, toolkit.CallContext{
		RunID: "caller-run", StepID: "caller-step", UserID: "caller-user",
		Scopes: []string{"admin"}, IdempotencyKey: "caller-key",
	})
	executor, err := NewMCPExecutor(callerContext, MCPConfig{
		Endpoint: testServer.URL, GatewayToken: testGatewayToken,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelConstructor()

	started := time.Now()
	err = executor.Close()
	elapsed := time.Since(started)
	if !errors.Is(err, ErrMCPTransport) {
		t.Fatalf("Close() error = %v, want ErrMCPTransport after cleanup timeout", err)
	}
	if elapsed < 25*time.Millisecond {
		t.Fatalf("Close() returned after %v; canceled constructor context aborted normal cleanup", elapsed)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("Close() returned after %v, want fixed cleanup timeout", elapsed)
	}
	transport.requireFinished(t)
	if initialErr := transport.deleteInitialContextError(); initialErr != nil {
		t.Fatalf("normal DELETE began with canceled context: %v", initialErr)
	}
	transport.requireNoTrustedHeaders(t)
	transport.requireNoCallerValues(t)
}

func TestExecutorCancellationTakesPriorityOverOversizedResponse(t *testing.T) {
	recorder := &callRecorder{}
	transport := &oversizedToolResponseTransport{next: http.DefaultTransport, mode: oversizedStreamed, cancel: true}
	executor := newTestExecutorWithClientOptions(t, recorder, &http.Client{Transport: transport}, false)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{{
		ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read,
	}}}, "user_018", []string{"order:read"})
	ctx, cancel := context.WithCancel(context.Background())
	transport.cancelCall = cancel

	result, err := executor.Execute(ctx, ExecutionRequest{RunID: "run-cancel-oversized", Plan: plan})
	if !errors.Is(err, context.Canceled) || result.Status != StatusFailed {
		t.Fatalf("Execute() = %#v, %v; want failed/context.Canceled", result, err)
	}
	_ = executor.Close()
}

func TestExecutorCloseClosesDiscardedDeleteResponseBodyAndPreservesStatus(t *testing.T) {
	recorder := &callRecorder{}
	tracking := &trackingDeleteTransport{next: http.DefaultTransport, status: http.StatusTeapot, closed: make(chan struct{})}
	executor := newTestExecutorWithClientOptions(t, recorder, &http.Client{Transport: tracking}, false)

	if err := executor.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil for HTTP status response", err)
	}
	select {
	case <-tracking.closed:
	default:
		t.Fatal("Close() returned without closing the DELETE response body")
	}
}

func TestConcurrentCloseWaitsForSharedSuccessfulShutdown(t *testing.T) {
	recorder := &callRecorder{}
	blocking := newBlockingDeleteTransport(http.DefaultTransport, nil)
	executor := newTestExecutorWithClientOptions(t, recorder, &http.Client{Transport: blocking}, false)
	plan := forgePlan(agent.ActionPlan{Steps: []agent.Step{{
		ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), Risk: toolkit.Read,
	}}}, "user_018", []string{"order:read"})

	first := make(chan error, 1)
	go func() { first <- executor.Close() }()
	<-blocking.entered
	second := make(chan error, 1)
	go func() { second <- executor.Close() }()
	executeResult := make(chan error, 1)
	go func() {
		_, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-during-close", Plan: plan})
		executeResult <- err
	}()

	assertNoResult(t, first, "first Close")
	assertNoResult(t, second, "second Close")
	assertNoResult(t, executeResult, "Execute during Close")
	close(blocking.release)
	if err := <-first; err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := <-executeResult; !errors.Is(err, ErrExecutorClosed) {
		t.Fatalf("Execute() error = %v, want ErrExecutorClosed", err)
	}
	if err := executor.Close(); err != nil {
		t.Fatalf("cached Close() error = %v", err)
	}
	if _, err := executor.Execute(context.Background(), ExecutionRequest{RunID: "run-after-close", Plan: plan}); !errors.Is(err, ErrExecutorClosed) {
		t.Fatalf("Execute() after Close error = %v, want ErrExecutorClosed", err)
	}
	if calls := recorder.snapshot(); len(calls) != 0 {
		t.Fatalf("tool calls during close = %d, want zero", len(calls))
	}
}

func TestConcurrentCloseWaitsForAndCachesSanitizedError(t *testing.T) {
	recorder := &callRecorder{}
	rawErr := errors.New("delete leaked gateway secret")
	blocking := newBlockingDeleteTransport(http.DefaultTransport, rawErr)
	executor := newTestExecutorWithClientOptions(t, recorder, &http.Client{Transport: blocking}, false)

	first := make(chan error, 1)
	go func() { first <- executor.Close() }()
	<-blocking.entered
	second := make(chan error, 1)
	go func() { second <- executor.Close() }()
	assertNoResult(t, first, "first Close")
	assertNoResult(t, second, "second Close")
	close(blocking.release)

	firstErr := <-first
	secondErr := <-second
	thirdErr := executor.Close()
	for index, err := range []error{firstErr, secondErr, thirdErr} {
		if err != ErrMCPTransport {
			t.Fatalf("Close result %d = %v, want cached ErrMCPTransport", index, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("Close result %d leaked raw transport error: %v", index, err)
		}
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
	return newTestExecutorWithClientOptions(t, recorder, client, true)
}

func newTestExecutorWithClientOptions(t *testing.T, recorder *callRecorder, client *http.Client, cleanupExecutor bool) *MCPExecutor {
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
	if cleanupExecutor {
		t.Cleanup(func() {
			if err := executor.Close(); err != nil {
				t.Errorf("close executor: %v", err)
			}
		})
	}
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

type oversizedResponseMode uint8

const (
	oversizedDeclared oversizedResponseMode = iota
	oversizedStreamed
)

type oversizedToolResponseTransport struct {
	next       http.RoundTripper
	mode       oversizedResponseMode
	cancel     bool
	cancelCall context.CancelFunc
}

type oversizedInitializeTransport struct {
	next    http.RoundTripper
	mode    oversizedResponseMode
	cancel  context.CancelFunc
	mu      sync.Mutex
	headers http.Header
}

type attemptCountingTransport struct {
	attempts atomic.Int64
}

func (transport *attemptCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.attempts.Add(1)
	return nil, errors.New("unexpected transport attempt")
}

const cleanupTransportSafetyTimeout = 750 * time.Millisecond

type cleanupDeadlineTransport struct {
	next                 http.RoundTripper
	oversizedInitialize  bool
	mode                 oversizedResponseMode
	deleteEntered        chan struct{}
	deleteExited         chan struct{}
	initializeBodyClosed chan struct{}
	mu                   sync.Mutex
	initializeHeaders    http.Header
	deleteHeaders        http.Header
	callerValueKey       any
	initializeValue      any
	deleteValue          any
	deleteInitialErr     error
	enterOnce            sync.Once
	exitOnce             sync.Once
	bodyCloseOnce        sync.Once
}

func newCleanupDeadlineTransport(next http.RoundTripper, oversizedInitialize bool, mode oversizedResponseMode, callerValueKey any) *cleanupDeadlineTransport {
	return &cleanupDeadlineTransport{
		next: next, oversizedInitialize: oversizedInitialize, mode: mode,
		deleteEntered: make(chan struct{}), deleteExited: make(chan struct{}),
		initializeBodyClosed: make(chan struct{}),
		callerValueKey:       callerValueKey,
	}
}

func (transport *cleanupDeadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodDelete {
		transport.mu.Lock()
		transport.deleteHeaders = request.Header.Clone()
		transport.deleteValue = request.Context().Value(transport.callerValueKey)
		transport.deleteInitialErr = request.Context().Err()
		transport.mu.Unlock()
		transport.enterOnce.Do(func() { close(transport.deleteEntered) })
		defer transport.exitOnce.Do(func() { close(transport.deleteExited) })
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(cleanupTransportSafetyTimeout):
			return nil, errors.New("test cleanup safety timeout")
		}
	}

	initialize := false
	if request.Method == http.MethodPost && request.Body != nil {
		requestBody, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		request.Body = io.NopCloser(bytes.NewReader(requestBody))
		initialize = bytes.Contains(requestBody, []byte(`"method":"initialize"`))
		if initialize {
			transport.mu.Lock()
			transport.initializeHeaders = request.Header.Clone()
			transport.initializeValue = request.Context().Value(transport.callerValueKey)
			transport.mu.Unlock()
		}
	}
	response, err := transport.next.RoundTrip(request)
	if err != nil || !transport.oversizedInitialize || !initialize {
		return response, err
	}
	closed := func() { transport.bodyCloseOnce.Do(func() { close(transport.initializeBodyClosed) }) }
	switch transport.mode {
	case oversizedDeclared:
		response.Body = &observedReadCloser{ReadCloser: response.Body, onClose: closed}
		response.ContentLength = maxMCPResponseBytes + 1
		response.Header.Set("Content-Length", "1048577")
	case oversizedStreamed:
		_ = response.Body.Close()
		response.Body = &observedReadCloser{
			ReadCloser: io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), int(maxMCPResponseBytes+1)))),
			onClose:    closed,
		}
		response.ContentLength = -1
		response.Header.Del("Content-Length")
	}
	return response, nil
}

func (transport *cleanupDeadlineTransport) requireNoCallerValues(t *testing.T) {
	t.Helper()
	transport.mu.Lock()
	initializeValue := transport.initializeValue
	deleteValue := transport.deleteValue
	transport.mu.Unlock()
	if initializeValue != nil {
		t.Fatalf("initialize transport context leaked caller value %#v", initializeValue)
	}
	if deleteValue != nil {
		t.Fatalf("DELETE transport context leaked caller value %#v", deleteValue)
	}
}

func (transport *cleanupDeadlineTransport) requireFinished(t *testing.T) {
	t.Helper()
	select {
	case <-transport.deleteEntered:
	default:
		t.Fatal("cleanup DELETE was not issued")
	}
	select {
	case <-transport.deleteExited:
	default:
		t.Fatal("cleanup DELETE goroutine did not exit")
	}
}

func (transport *cleanupDeadlineTransport) requireNoTrustedHeaders(t *testing.T) {
	t.Helper()
	transport.mu.Lock()
	initializeHeaders := transport.initializeHeaders.Clone()
	deleteHeaders := transport.deleteHeaders.Clone()
	transport.mu.Unlock()
	for phase, headers := range map[string]http.Header{"initialize": initializeHeaders, "DELETE": deleteHeaders} {
		if got := headers.Get("Authorization"); got != "Bearer "+testGatewayToken {
			t.Fatalf("%s Authorization = %q", phase, got)
		}
		for _, header := range trustedRequestHeaders[2:] {
			if got := headers.Get(header); got != "" {
				t.Fatalf("%s leaked trusted header %s=%q", phase, header, got)
			}
		}
	}
}

func (transport *cleanupDeadlineTransport) requireInitializeBodyClosed(t *testing.T) {
	t.Helper()
	select {
	case <-transport.initializeBodyClosed:
	default:
		t.Fatal("oversized initialize response body was not closed")
	}
}

func (transport *cleanupDeadlineTransport) deleteInitialContextError() error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.deleteInitialErr
}

type trackingAfterFuncContext struct {
	deadline time.Time
	done     chan struct{}
	valueKey any
	value    any
	stopped  chan struct{}
	stopOnce sync.Once
}

type cancelDuringAfterFuncContext struct {
	done       chan struct{}
	registered atomic.Bool
}

func (*cancelDuringAfterFuncContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelDuringAfterFuncContext) Done() <-chan struct{}   { return ctx.done }
func (ctx *cancelDuringAfterFuncContext) Err() error {
	if ctx.registered.Load() {
		return context.Canceled
	}
	return nil
}
func (*cancelDuringAfterFuncContext) Value(any) any { return nil }

func (ctx *cancelDuringAfterFuncContext) AfterFunc(func()) func() bool {
	ctx.registered.Store(true)
	return func() bool { return true }
}

func (ctx *trackingAfterFuncContext) Deadline() (time.Time, bool) { return ctx.deadline, true }
func (ctx *trackingAfterFuncContext) Done() <-chan struct{}       { return ctx.done }
func (*trackingAfterFuncContext) Err() error                      { return nil }

func (ctx *trackingAfterFuncContext) Value(key any) any {
	if key == ctx.valueKey {
		return ctx.value
	}
	return nil
}

func (ctx *trackingAfterFuncContext) AfterFunc(func()) func() bool {
	return func() bool {
		stopped := false
		ctx.stopOnce.Do(func() {
			stopped = true
			close(ctx.stopped)
		})
		return stopped
	}
}

type observedReadCloser struct {
	io.ReadCloser
	onClose func()
}

func (body *observedReadCloser) Close() error {
	err := body.ReadCloser.Close()
	body.onClose()
	return err
}

func (transport *oversizedInitializeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || request.Body == nil {
		return transport.next.RoundTrip(request)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	initialize := request.Method == http.MethodPost && bytes.Contains(body, []byte(`"method":"initialize"`))
	response, err := transport.next.RoundTrip(request)
	if err != nil || !initialize {
		return response, err
	}
	transport.mu.Lock()
	transport.headers = request.Header.Clone()
	transport.mu.Unlock()
	if transport.cancel != nil {
		transport.cancel()
	}
	switch transport.mode {
	case oversizedDeclared:
		response.ContentLength = maxMCPResponseBytes + 1
		response.Header.Set("Content-Length", "1048577")
	case oversizedStreamed:
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), int(maxMCPResponseBytes+1))))
		response.ContentLength = -1
		response.Header.Del("Content-Length")
	}
	return response, nil
}

func (transport *oversizedInitializeTransport) initializeHeaders() http.Header {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.headers.Clone()
}

func (transport *oversizedToolResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.next.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if _, err := toolkit.FromContext(request.Context()); err != nil || request.Method != http.MethodPost {
		return response, nil
	}
	if transport.cancel && transport.cancelCall != nil {
		transport.cancelCall()
	}
	switch transport.mode {
	case oversizedDeclared:
		response.ContentLength = maxMCPResponseBytes + 1
		response.Header.Set("Content-Length", "1048577")
	case oversizedStreamed:
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(bytes.Repeat([]byte("x"), int(maxMCPResponseBytes+1))))
		response.ContentLength = -1
		response.Header.Del("Content-Length")
	}
	return response, nil
}

type trackingDeleteTransport struct {
	next   http.RoundTripper
	status int
	closed chan struct{}
	once   sync.Once
}

func (transport *trackingDeleteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodDelete {
		return transport.next.RoundTrip(request)
	}
	body := &trackingBody{closed: transport.closed, once: &transport.once}
	return &http.Response{
		StatusCode: transport.status,
		Status:     http.StatusText(transport.status),
		Header:     make(http.Header),
		Body:       body,
		Request:    request,
	}, nil
}

type trackingBody struct {
	closed chan struct{}
	once   *sync.Once
}

func (*trackingBody) Read([]byte) (int, error) { return 0, io.EOF }

func (body *trackingBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

type blockingDeleteTransport struct {
	next    http.RoundTripper
	err     error
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingDeleteTransport(next http.RoundTripper, err error) *blockingDeleteTransport {
	return &blockingDeleteTransport{next: next, err: err, entered: make(chan struct{}), release: make(chan struct{})}
}

func (transport *blockingDeleteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodDelete {
		return transport.next.RoundTrip(request)
	}
	transport.once.Do(func() { close(transport.entered) })
	<-transport.release
	if transport.err != nil {
		return nil, transport.err
	}
	return transport.next.RoundTrip(request)
}

func assertNoResult(t *testing.T, result <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("%s returned early with %v", operation, err)
	case <-time.After(50 * time.Millisecond):
	}
}
