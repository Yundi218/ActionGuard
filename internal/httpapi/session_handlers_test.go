package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/auth"
	"github.com/Yundi218/ActionGuard/internal/orchestrator"
)

type fakeAuthenticator struct {
	principal auth.Principal
	err       error
	calls     int
}

type readErrorBody struct{ err error }

func (body readErrorBody) Read([]byte) (int, error) { return 0, body.err }
func (readErrorBody) Close() error                  { return nil }

func (f *fakeAuthenticator) Authenticate(*http.Request) (auth.Principal, error) {
	f.calls++
	return f.principal, f.err
}

type fakeSessionService struct {
	principal                         auth.Principal
	region, sessionID, message, runID string
	view                              orchestrator.RunView
	err                               error
	calls                             int
}

func (f *fakeSessionService) CreateSession(_ context.Context, p auth.Principal, region string) (orchestrator.SessionView, error) {
	f.calls++
	f.principal = p
	f.region = region
	return orchestrator.SessionView{SessionID: "session-id", Region: region, CreatedAt: time.Unix(0, 0).UTC()}, f.err
}
func (f *fakeSessionService) RunMessage(_ context.Context, p auth.Principal, id, message string) (orchestrator.RunView, error) {
	f.calls++
	f.principal = p
	f.sessionID = id
	f.message = message
	return f.view, f.err
}
func (f *fakeSessionService) GetRun(_ context.Context, p auth.Principal, id string) (orchestrator.RunView, error) {
	f.calls++
	f.principal = p
	f.runID = id
	return f.view, f.err
}

func TestSessionRoutesAuthenticateAndUseOnlyPrincipalIdentity(t *testing.T) {
	authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
	service := &fakeSessionService{view: orchestrator.RunView{RunID: "run", SessionID: "session", Status: "succeeded", Evidence: []orchestrator.EvidenceView{}}}
	router := NewRouter(Dependencies{Authenticator: authenticator, Sessions: service})
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions/session/messages", strings.NewReader(`{"message":"  Order AG-1042  "}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ActionGuard-User", "user_999")
	req.Header.Set("X-ActionGuard-Scopes", "refund:write")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body=%d %s", recorder.Code, recorder.Body.String())
	}
	if authenticator.calls != 1 || service.calls != 1 || service.principal.UserID != "user_018" || service.message != "  Order AG-1042  " {
		t.Fatalf("auth=%d service=%#v", authenticator.calls, service)
	}
}

func TestSessionRoutesAuthenticateBeforeParsing(t *testing.T) {
	authenticator := &fakeAuthenticator{err: auth.ErrUnauthenticated}
	service := &fakeSessionService{}
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"region":`))
	req.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != `{"error":{"code":"unauthenticated"}}`+"\n" || service.calls != 0 {
		t.Fatalf("status/body/calls=%d %q %d", recorder.Code, recorder.Body.String(), service.calls)
	}
}

func TestStrictJSONRequests(t *testing.T) {
	tests := []struct {
		name, body, contentType string
		want                    int
	}{
		{name: "unknown", body: `{"region":"CN","user_id":"spoof"}`, contentType: "application/json", want: 400},
		{name: "case alias", body: `{"REGION":"CN"}`, contentType: "application/json", want: 400},
		{name: "duplicate nested", body: `{"message":"ok","extra":{"x":1,"x":2}}`, contentType: "application/json", want: 400},
		{name: "multiple", body: `{"region":"CN"} {}`, contentType: "application/json", want: 400},
		{name: "missing", body: `{}`, contentType: "application/json", want: 400},
		{name: "unsupported", body: `{"region":"CN"}`, contentType: "text/plain", want: 415},
		{name: "too large", body: `{"region":"` + strings.Repeat("x", 1<<20) + `"}`, contentType: "application/json", want: 413},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
			service := &fakeSessionService{}
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer token")
			req.Header.Set("Content-Type", test.contentType)
			rec := httptest.NewRecorder()
			NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(rec, req)
			if rec.Code != test.want || service.calls != 0 {
				t.Fatalf("status/body/calls=%d %q %d", rec.Code, rec.Body.String(), service.calls)
			}
		})
	}
}

func TestStrictJSONRejectsInvalidUTF8(t *testing.T) {
	authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
	service := &fakeSessionService{}
	body := append([]byte(`{"region":"`), 0xff)
	body = append(body, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(rec, req)
	if rec.Code != 400 || service.calls != 0 {
		t.Fatalf("status/body/calls=%d %q %d", rec.Code, rec.Body.String(), service.calls)
	}
}

func TestCanceledRequestBodyReadReturnsStable408(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
			service := &fakeSessionService{}
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
			req.Body = readErrorBody{err: cause}
			req.Header.Set("Authorization", "Bearer token")
			req.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(recorder, req)
			if recorder.Code != http.StatusRequestTimeout || recorder.Body.String() != `{"error":{"code":"request_canceled"}}`+"\n" || service.calls != 0 {
				t.Fatalf("status/body/calls=%d %q %d", recorder.Code, recorder.Body.String(), service.calls)
			}
		})
	}

	t.Run("request context already canceled", func(t *testing.T) {
		authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
		service := &fakeSessionService{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"region":"CN"}`)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(recorder, req)
		if recorder.Code != http.StatusRequestTimeout || recorder.Body.String() != `{"error":{"code":"request_canceled"}}`+"\n" || strings.Contains(recorder.Body.String(), context.Canceled.Error()) {
			t.Fatalf("status/body=%d %q", recorder.Code, recorder.Body.String())
		}
	})
}

func TestHTTPErrorMappingAndCompletedFailedView(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{{"not found", orchestrator.ErrNotFound, 404}, {"conflict", orchestrator.ErrConflict, 409}, {"canceled", orchestrator.ErrCanceled, 408}, {"invalid", orchestrator.ErrInvalid, 400}, {"internal", orchestrator.ErrService, 500}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
			service := &fakeSessionService{err: test.err}
			req := httptest.NewRequest(http.MethodGet, "/v1/runs/run", nil)
			req.Header.Set("Authorization", "Bearer token")
			rec := httptest.NewRecorder()
			NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status/body=%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	authenticator := &fakeAuthenticator{principal: auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}}
	service := &fakeSessionService{view: orchestrator.RunView{RunID: "run", SessionID: "session", Status: "failed", Evidence: []orchestrator.EvidenceView{}}}
	req := httptest.NewRequest(http.MethodGet, "/v1/runs/run", nil)
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	NewRouter(Dependencies{Authenticator: authenticator, Sessions: service}).ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"failed"`) {
		t.Fatalf("status/body=%d %s", rec.Code, rec.Body.String())
	}
}
