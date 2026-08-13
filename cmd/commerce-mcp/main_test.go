package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/mcpserver"
)

func TestNewCommerceMCPServerWiresSharedTimeoutsAndHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	server := newCommerceMCPServer("127.0.0.1:18081", handler)
	if server.Addr != "127.0.0.1:18081" {
		t.Fatalf("address = %q", server.Addr)
	}
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 15*time.Second || server.WriteTimeout != 30*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatalf("timeouts = header:%s read:%s write:%s idle:%s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("handler status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestMainDelegatesToCommerceMCPServerFactory(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "main" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			identifier, ok := call.Fun.(*ast.Ident)
			if ok && identifier.Name == "newCommerceMCPServer" {
				called = true
			}
			return true
		})
	}
	if !called {
		t.Fatal("main must delegate to newCommerceMCPServer instead of calling a server directly")
	}
}

const testMCPBodyLimit = 1 << 20

type countingBody struct {
	reader      io.Reader
	read        int64
	maxReadSize int
	closed      bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	if len(p) > b.maxReadSize {
		b.maxReadSize = len(p)
	}
	n, err := b.reader.Read(p)
	b.read += int64(n)
	return n, err
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

func TestMCPHandlerReturns413ForKnownContentLength(t *testing.T) {
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", testMCPBodyLimit+1))}
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	request.Body = body
	request.ContentLength = testMCPBodyLimit + 1
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()

	newMCPHandler(mcpserver.New(nil), "gateway-secret").ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusRequestEntityTooLarge)
	}
	if body.read != testMCPBodyLimit+1 {
		t.Fatalf("body bytes read = %d, want %d", body.read, testMCPBodyLimit+1)
	}
	if !body.closed {
		t.Fatal("original request body was not closed")
	}
}

func TestMCPHandlerReturns413ForUnknownChunkedLength(t *testing.T) {
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
	if !body.closed {
		t.Fatal("original request body was not closed")
	}
}

func TestMCPMuxPassesExactlyOneMiBBodyToDownstream(t *testing.T) {
	var got []byte
	body := &countingBody{reader: strings.NewReader(strings.Repeat("x", testMCPBodyLimit))}
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == body {
			t.Fatal("downstream received original request body instead of restored body")
		}
		var err error
		got, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	request.Body = body
	request.Header.Set("Authorization", "Bearer gateway-secret")
	response := httptest.NewRecorder()

	newMCPMux("gateway-secret", downstream).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if string(got) != strings.Repeat("x", testMCPBodyLimit) {
		t.Fatalf("downstream body length = %d, want %d", len(got), testMCPBodyLimit)
	}
	if !body.closed {
		t.Fatal("original request body was not closed")
	}
}

func TestLimitRequestBodyDoesNotPreallocateMaximumBuffer(t *testing.T) {
	body := &countingBody{reader: strings.NewReader("x")}
	request := httptest.NewRequest(http.MethodPost, "/mcp", body)
	request.Body = body
	response := httptest.NewRecorder()
	called := false

	limitRequestBody(testMCPBodyLimit, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	})).ServeHTTP(response, request)

	if !called {
		t.Fatal("downstream handler was not called")
	}
	if body.maxReadSize >= testMCPBodyLimit {
		t.Fatalf("largest read buffer = %d, want dynamic buffer smaller than %d", body.maxReadSize, testMCPBodyLimit)
	}
	if !body.closed {
		t.Fatal("original request body was not closed")
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
