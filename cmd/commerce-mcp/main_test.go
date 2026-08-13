package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPHandlerCapsRequestBodyAtOneMiB(t *testing.T) {
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			var maxBytesError *http.MaxBytesError
			if errors.As(err, &maxBytesError) {
				http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
				return
			}
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := newMCPMux("gateway-secret", downstream)

	for _, tt := range []struct {
		name       string
		size       int
		wantStatus int
	}{
		{name: "at limit", size: 1 << 20, wantStatus: http.StatusNoContent},
		{name: "over limit", size: 1<<20 + 1, wantStatus: http.StatusRequestEntityTooLarge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", tt.size)))
			request.Header.Set("Authorization", "Bearer gateway-secret")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
		})
	}
}
