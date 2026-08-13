package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/Yundi218/ActionGuard/internal/verifier"
)

const (
	testAPIKey        = "secret-openai-key"
	testUserMessage   = "private user content"
	testEvidenceText  = "private evidence content"
	testResponseModel = "fixture-response-model"
)

func TestOpenAIPlannerSendsStrictResponsesRequestAndTraversesOutput(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" {
			t.Errorf("request = %s %s, want POST /v1/responses", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testAPIKey {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writePlanResponse(t, writer, lookupPlanJSON())
	}))
	t.Cleanup(server.Close)

	transport := &deadlineTransport{next: http.DefaultTransport}
	planner := newTestPlanner(t, server.URL, &http.Client{Transport: transport}, nil)
	evidence := []policy.Evidence{{
		CitationID: "damaged_goods:v3:Replacement eligibility:1-2",
		PolicyID:   "damaged_goods", Version: "v3", Text: testEvidenceText,
	}}
	plan, err := planner.Plan(context.Background(), PlanRequest{
		UserMessage: testUserMessage, Evidence: evidence, ToolContracts: toolkit.Registry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !transport.sawDeadline.Load() {
		t.Fatal("outgoing request context did not have a deadline")
	}
	if plan.Goal != "look up order" || len(plan.Steps) != 1 || plan.Steps[0].Tool != "get_order" {
		t.Fatalf("plan = %#v", plan)
	}
	assertStructuredPlannerRequest(t, requestBody, testUserMessage, testEvidenceText)
}

func TestOpenAIPlannerRepairSendsStructuredVerifierErrors(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		writePlanResponse(t, writer, lookupPlanJSON())
	}))
	t.Cleanup(server.Close)

	planner := newTestPlanner(t, server.URL, nil, nil)
	original := PlanRequest{UserMessage: FixtureOrderLookupMessage, ToolContracts: toolkit.Registry()}
	prior, err := agent.DecodeActionPlan(lookupPlanJSON())
	if err != nil {
		t.Fatal(err)
	}
	verificationErrors := []verifier.Error{{Code: "missing_scope", StepID: "order", Field: "scope", Message: "required tool scope is missing"}}
	if _, err := planner.Repair(context.Background(), RepairRequest{Original: original, Plan: prior, Errors: verificationErrors}); err != nil {
		t.Fatal(err)
	}

	items := requestInputItems(t, requestBody)
	var found bool
	for _, item := range items {
		text := inputText(t, item)
		var value map[string]any
		if json.Unmarshal([]byte(text), &value) == nil && value["label"] == "structured_verifier_repair" {
			found = true
			if !strings.Contains(text, "missing_scope") || !strings.Contains(text, "required tool scope is missing") {
				t.Fatalf("repair input = %s", text)
			}
		}
	}
	if !found {
		t.Fatal("structured repair input item not found")
	}
}

func TestOpenAIPlannerRejectsInvalidResponsesWithoutRetryOrLeakage(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       error
	}{
		{name: "refusal", statusCode: http.StatusOK, body: `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"body-secret refusal"}]}]}`, want: ErrRefusal},
		{name: "incomplete", statusCode: http.StatusOK, body: `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, want: ErrIncompleteResponse},
		{name: "zero output text", statusCode: http.StatusOK, body: `{"status":"completed","output":[{"type":"reasoning"}]}`, want: ErrInvalidResponse},
		{name: "multiple output text", statusCode: http.StatusOK, body: fmt.Sprintf(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q},{"type":"output_text","text":%q}]}]}`, lookupPlanJSON(), lookupPlanJSON()), want: ErrInvalidResponse},
		{name: "invalid plan json", statusCode: http.StatusOK, body: `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"body-secret not-json"}]}]}`, want: ErrInvalidResponse},
		{name: "unknown plan field", statusCode: http.StatusOK, body: `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"goal\":\"lookup\",\"policy_refs\":[],\"steps\":[],\"body-secret\":true}"}]}]}`, want: ErrInvalidResponse},
		{name: "duplicate status", statusCode: http.StatusOK, body: fmt.Sprintf(`{"status":"incomplete","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, lookupPlanJSON()), want: ErrInvalidResponse},
		{name: "duplicate output", statusCode: http.StatusOK, body: fmt.Sprintf(`{"status":"completed","output":[],"output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}`, lookupPlanJSON()), want: ErrInvalidResponse},
		{name: "duplicate content type masks refusal", statusCode: http.StatusOK, body: fmt.Sprintf(`{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","type":"output_text","refusal":"body-secret refusal","text":%q}]}]}`, lookupPlanJSON()), want: ErrInvalidResponse},
		{name: "duplicate refusal", statusCode: http.StatusOK, body: fmt.Sprintf(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","refusal":"body-secret first","refusal":"body-secret second","text":%q}]}]}`, lookupPlanJSON()), want: ErrInvalidResponse},
		{name: "non retryable status", statusCode: http.StatusBadRequest, body: `body-secret invalid request`, want: ErrRequestFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				writer.WriteHeader(test.statusCode)
				_, _ = writer.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)

			planner := newTestPlanner(t, server.URL, nil, nil)
			_, err := planner.Plan(context.Background(), PlanRequest{UserMessage: testUserMessage, ToolContracts: toolkit.Registry()})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1", attempts.Load())
			}
			assertSanitizedError(t, err)
		})
	}
}

func TestOpenAIPlannerCapsResponseBodiesAtOneMiB(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		_, _ = writer.Write([]byte(strings.Repeat("body-secret", maxResponseBodyBytes/len("body-secret")+2)))
	}))
	t.Cleanup(server.Close)

	planner := newTestPlanner(t, server.URL, nil, nil)
	_, err := planner.Plan(context.Background(), PlanRequest{UserMessage: testUserMessage, ToolContracts: toolkit.Registry()})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
	assertSanitizedError(t, err)
}

func TestOpenAIPlannerRetriesTransientStatusesAtMostTwice(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempt := attempts.Add(1)
				if attempt < 3 {
					writer.WriteHeader(status)
					_, _ = writer.Write([]byte("body-secret transient"))
					return
				}
				writePlanResponse(t, writer, lookupPlanJSON())
			}))
			t.Cleanup(server.Close)

			var backoffs atomic.Int32
			planner := newTestPlanner(t, server.URL, nil, func(context.Context, int) error {
				backoffs.Add(1)
				return nil
			})
			if _, err := planner.Plan(context.Background(), PlanRequest{UserMessage: FixtureOrderLookupMessage, ToolContracts: toolkit.Registry()}); err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 3 || backoffs.Load() != 2 {
				t.Fatalf("attempts/backoffs = %d/%d, want 3/2", attempts.Load(), backoffs.Load())
			}
		})
	}
}

func TestOpenAIPlannerRetriesAttemptTimeoutAtMostTwice(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		time.Sleep(50 * time.Millisecond)
		writePlanResponse(t, writer, lookupPlanJSON())
	}))
	t.Cleanup(server.Close)

	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		BaseURL: server.URL, APIKey: testAPIKey, Model: testResponseModel,
		RequestTimeout: 10 * time.Millisecond,
		Backoff:        func(context.Context, int) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = planner.Plan(context.Background(), PlanRequest{UserMessage: FixtureOrderLookupMessage, ToolContracts: toolkit.Registry()})
	if !errors.Is(err, ErrRequestFailed) {
		t.Fatalf("error = %v, want ErrRequestFailed", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestOpenAIPlannerStopsWhenCallerCancelsDuringBackoff(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	backoffStarted := make(chan struct{})
	var once sync.Once
	planner := newTestPlanner(t, server.URL, nil, func(ctx context.Context, _ int) error {
		once.Do(func() { close(backoffStarted) })
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := planner.Plan(ctx, PlanRequest{UserMessage: FixtureOrderLookupMessage, ToolContracts: toolkit.Registry()})
		result <- err
	}()
	<-backoffStarted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestOpenAIPlannerDoesNotAttemptCanceledCallerContext(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { attempts.Add(1) }))
	t.Cleanup(server.Close)
	planner := newTestPlanner(t, server.URL, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := planner.Plan(ctx, PlanRequest{UserMessage: FixtureOrderLookupMessage})
	if !errors.Is(err, context.Canceled) || attempts.Load() != 0 {
		t.Fatalf("error/attempts = %v/%d, want context.Canceled/0", err, attempts.Load())
	}
}

func TestOpenAIEmbedderSendsOneBatched1536DimensionRequest(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/embeddings" {
			t.Errorf("request = %s %s, want POST /v1/embeddings", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAPIKey {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		data := []map[string]any{
			{"index": 1, "embedding": finiteVector(2)},
			{"index": 0, "embedding": finiteVector(1)},
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)

	transport := &deadlineTransport{next: http.DefaultTransport}
	embedder := newTestEmbedder(t, server.URL, &http.Client{Transport: transport}, nil)
	vectors, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	if !transport.sawDeadline.Load() {
		t.Fatal("outgoing request context did not have a deadline")
	}
	if len(vectors) != 2 || len(vectors[0]) != policy.EmbeddingDimensions || len(vectors[1]) != policy.EmbeddingDimensions {
		t.Fatalf("vector dimensions = %d/%d/%d", len(vectors), len(vectors[0]), len(vectors[1]))
	}
	if vectors[0][0] != 1 || vectors[1][0] != 2 {
		t.Fatalf("vectors were not restored by index: %v/%v", vectors[0][0], vectors[1][0])
	}
	if requestBody["model"] != "fixture-embedding-model" || requestBody["dimensions"] != float64(policy.EmbeddingDimensions) {
		t.Fatalf("embedding request = %#v", requestBody)
	}
	input, ok := requestBody["input"].([]any)
	if !ok || len(input) != 2 || input[0] != "first" || input[1] != "second" {
		t.Fatalf("embedding input = %#v", requestBody["input"])
	}
}

func TestOpenAIEmbedderRejectsMalformedVectorsWithoutLeakage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "wrong width", body: `{"data":[{"index":0,"embedding":[1]}]}`},
		{name: "missing vector", body: `{"data":[]}`},
		{name: "missing index", body: fmt.Sprintf(`{"data":[{"embedding":%s}]}`, vectorJSON(t, finiteVector(1)))},
		{name: "duplicate index member", body: fmt.Sprintf(`{"data":[{"index":1,"index":0,"embedding":%s}]}`, vectorJSON(t, finiteVector(1)))},
		{name: "duplicate index", body: fmt.Sprintf(`{"data":[{"index":0,"embedding":%s},{"index":0,"embedding":%s}]}`, vectorJSON(t, finiteVector(1)), vectorJSON(t, finiteVector(2)))},
		{name: "non finite", body: `{"data":[{"index":0,"embedding":[1e999]}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				attempts.Add(1)
				_, _ = writer.Write([]byte(strings.ReplaceAll(test.body, "finite-secret", "body-secret")))
			}))
			t.Cleanup(server.Close)
			embedder := newTestEmbedder(t, server.URL, nil, nil)
			_, err := embedder.Embed(context.Background(), []string{"private embedding input"})
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("error = %v, want ErrInvalidResponse", err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1", attempts.Load())
			}
			assertSanitizedError(t, err)
		})
	}
}

func TestOpenAIEmbedderRetriesTransientFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": finiteVector(1)}}})
	}))
	t.Cleanup(server.Close)
	embedder := newTestEmbedder(t, server.URL, nil, func(context.Context, int) error { return nil })
	if _, err := embedder.Embed(context.Background(), []string{"retry"}); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
}

func TestOpenAIConstructorsRequireExplicitConfiguration(t *testing.T) {
	plannerTests := []OpenAIPlannerConfig{
		{},
		{APIKey: testAPIKey},
		{APIKey: testAPIKey, Model: testResponseModel, BaseURL: "://bad"},
	}
	for _, config := range plannerTests {
		if _, err := NewOpenAIPlanner(config); !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), testAPIKey) {
			t.Fatalf("planner config error = %v", err)
		}
	}
	embedderTests := []OpenAIEmbeddingConfig{
		{},
		{APIKey: testAPIKey},
		{APIKey: testAPIKey, Model: "fixture-embedding-model", BaseURL: "://bad"},
	}
	for _, config := range embedderTests {
		if _, err := NewOpenAIEmbedder(config); !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), testAPIKey) {
			t.Fatalf("embedder config error = %v", err)
		}
	}
}

type deadlineTransport struct {
	next        http.RoundTripper
	sawDeadline atomic.Bool
}

func (transport *deadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if _, ok := request.Context().Deadline(); ok {
		transport.sawDeadline.Store(true)
	}
	return transport.next.RoundTrip(request)
}

func newTestPlanner(t *testing.T, baseURL string, client *http.Client, backoff BackoffFunc) *OpenAIPlanner {
	t.Helper()
	planner, err := NewOpenAIPlanner(OpenAIPlannerConfig{
		BaseURL: baseURL, APIKey: testAPIKey, Model: testResponseModel,
		HTTPClient: client, RequestTimeout: time.Second, Backoff: backoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	return planner
}

func newTestEmbedder(t *testing.T, baseURL string, client *http.Client, backoff BackoffFunc) *OpenAIEmbedder {
	t.Helper()
	embedder, err := NewOpenAIEmbedder(OpenAIEmbeddingConfig{
		BaseURL: baseURL, APIKey: testAPIKey, Model: "fixture-embedding-model",
		HTTPClient: client, RequestTimeout: time.Second, Backoff: backoff,
	})
	if err != nil {
		t.Fatal(err)
	}
	return embedder
}

func assertStructuredPlannerRequest(t *testing.T, body map[string]any, userMessage, evidenceText string) {
	t.Helper()
	if body["model"] != testResponseModel {
		t.Fatalf("model = %#v", body["model"])
	}
	textConfig, ok := body["text"].(map[string]any)
	if !ok {
		t.Fatalf("text = %#v", body["text"])
	}
	format, ok := textConfig["format"].(map[string]any)
	if !ok || format["type"] != "json_schema" || format["name"] != "action_plan" || format["strict"] != true {
		t.Fatalf("text.format = %#v", textConfig["format"])
	}
	gotSchema, err := json.Marshal(format["schema"])
	if err != nil {
		t.Fatal(err)
	}
	var wantSchema any
	if err := json.Unmarshal(agent.ActionPlanSchema(), &wantSchema); err != nil {
		t.Fatal(err)
	}
	removeUnsupportedOpenAISchemaKeywords(wantSchema)
	wantSchemaJSON, err := json.Marshal(wantSchema)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSchema) != string(wantSchemaJSON) {
		t.Fatalf("schema = %s, want %s", gotSchema, wantSchemaJSON)
	}

	items := requestInputItems(t, body)
	if len(items) != 4 {
		t.Fatalf("input item count = %d, want 4", len(items))
	}
	systemText := inputText(t, items[0])
	if items[0]["role"] != "system" || strings.Contains(systemText, userMessage) || strings.Contains(systemText, evidenceText) {
		t.Fatalf("system instruction contains untrusted content: %q", systemText)
	}
	for _, phrase := range []string{"untrusted", "minimum plan", "registry", "identity", "Scope", "idempotency"} {
		if !strings.Contains(systemText, phrase) {
			t.Errorf("system instruction missing %q: %q", phrase, systemText)
		}
	}

	assertLabeledJSONItem(t, items[2], "untrusted_user_message", "user_message", userMessage)
	assertLabeledJSONItem(t, items[3], "untrusted_policy_evidence", "evidence", evidenceText)
}

func removeUnsupportedOpenAISchemaKeywords(value any) {
	switch typed := value.(type) {
	case map[string]any:
		delete(typed, "minLength")
		delete(typed, "uniqueItems")
		for _, child := range typed {
			removeUnsupportedOpenAISchemaKeywords(child)
		}
	case []any:
		for _, child := range typed {
			removeUnsupportedOpenAISchemaKeywords(child)
		}
	}
}

func assertLabeledJSONItem(t *testing.T, item map[string]any, label, field, wantSubstring string) {
	t.Helper()
	if item["role"] != "user" {
		t.Fatalf("item role = %#v, want user", item["role"])
	}
	text := inputText(t, item)
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("input item is not standalone JSON: %v: %q", err, text)
	}
	if value["label"] != label || !strings.Contains(fmt.Sprint(value[field]), wantSubstring) {
		t.Fatalf("input item = %#v", value)
	}
}

func requestInputItems(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	rawItems, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v", body["input"])
	}
	items := make([]map[string]any, len(rawItems))
	for index, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("input[%d] = %#v", index, raw)
		}
		items[index] = item
	}
	return items
}

func inputText(t *testing.T, item map[string]any) string {
	t.Helper()
	content, ok := item["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v", item["content"])
	}
	part, ok := content[0].(map[string]any)
	if !ok || part["type"] != "input_text" {
		t.Fatalf("content part = %#v", content[0])
	}
	text, ok := part["text"].(string)
	if !ok {
		t.Fatalf("content text = %#v", part["text"])
	}
	return text
}

func writePlanResponse(t *testing.T, writer http.ResponseWriter, planJSON []byte) {
	t.Helper()
	response := map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{"type": "reasoning", "summary": []any{}},
			map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": string(planJSON)},
			}},
		},
	}
	if err := json.NewEncoder(writer).Encode(response); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func lookupPlanJSON() []byte {
	return []byte(`{"goal":"look up order","policy_refs":[],"steps":[{"id":"order","tool":"get_order","arguments":{"order_id":"AG-1042"},"depends_on":[],"risk":"read","success_condition":"order.exists","approval_required":false}]}`)
}

func finiteVector(first float32) []float32 {
	vector := make([]float32, policy.EmbeddingDimensions)
	vector[0] = first
	return vector
}

func vectorJSON(t *testing.T, vector []float32) string {
	t.Helper()
	data, err := json.Marshal(vector)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertSanitizedError(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{testAPIKey, testUserMessage, testEvidenceText, "body-secret", "private embedding input"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}
