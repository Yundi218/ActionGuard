package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Yundi218/ActionGuard/internal/mcpserver"
)

const testMCPBodyLimit = 1 << 20

type countingBody struct {
	reader io.Reader
	read   int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}

func (*countingBody) Close() error { return nil }

func TestMCPHandlerRejectsAuthenticatedOversizedRequestBeforeMCPParsing(t *testing.T) {
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", testMCPBodyLimit+1))}
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	request.Body = body
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()

	newMCPHandler(mcpserver.New(nil), "gateway-secret").ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if body.read != testMCPBodyLimit+1 {
		t.Fatalf("body bytes read = %d, want %d", body.read, testMCPBodyLimit+1)
	}
}

func TestMCPMuxPassesExactlyOneMiBBodyToDownstream(t *testing.T) {
	var got []byte
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	body := strings.Repeat("x", testMCPBodyLimit)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()

	newMCPMux("gateway-secret", downstream).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if string(got) != body {
		t.Fatalf("downstream body length = %d, want %d", len(got), len(body))
	}
}

func TestMCPHandlerRejectsUnauthenticatedOversizedRequestWithoutReadingBody(t *testing.T) {
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", testMCPBodyLimit+1))}
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	request.Body = body
	request.ContentLength = -1
	request.TransferEncoding = []string{"chunked"}
	response := httptest.NewRecorder()

	newMCPHandler(mcpserver.New(nil), "gateway-secret").ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if body.read != 0 {
		t.Fatalf("body bytes read = %d, want 0", body.read)
	}
}
