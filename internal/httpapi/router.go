package httpapi

import (
	"context"
	"net/http"

	"github.com/Yundi218/ActionGuard/internal/auth"
	"github.com/Yundi218/ActionGuard/internal/orchestrator"
	"github.com/go-chi/chi/v5"
)

type SessionService interface {
	CreateSession(context.Context, auth.Principal, string) (orchestrator.SessionView, error)
	RunMessage(context.Context, auth.Principal, string, string) (orchestrator.RunView, error)
	GetRun(context.Context, auth.Principal, string) (orchestrator.RunView, error)
}

type Dependencies struct {
	Authenticator auth.Authenticator
	Sessions      SessionService
}

func NewRouter(dependencies Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	handlers := sessionHandlers{authenticator: dependencies.Authenticator, sessions: dependencies.Sessions}
	r.Post("/v1/sessions", handlers.createSession)
	r.Post("/v1/sessions/{session_id}/messages", handlers.runMessage)
	r.Get("/v1/runs/{run_id}", handlers.getRun)
	return r
}
