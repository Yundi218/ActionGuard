package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewConfiguresExactTimeouts(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	server := New(":8081", handler)

	if server.Addr != ":8081" || server.Handler == nil {
		t.Fatalf("server address/handler = %q/%T", server.Addr, server.Handler)
	}
	server.Handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !called {
		t.Fatal("configured handler was not called")
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %s", server.ReadTimeout)
	}
	if server.WriteTimeout != 30*time.Second {
		t.Fatalf("WriteTimeout = %s", server.WriteTimeout)
	}
	if server.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s", server.IdleTimeout)
	}
}
