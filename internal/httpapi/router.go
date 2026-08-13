package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Dependencies struct{}

func NewRouter(_ Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return r
}
