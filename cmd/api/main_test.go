package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestNewAPIServerWiresSharedTimeoutsAndRouter(t *testing.T) {
	server := newAPIServer("127.0.0.1:18080")
	if server.Addr != "127.0.0.1:18080" {
		t.Fatalf("address = %q", server.Addr)
	}
	requireServerTimeouts(t, server)

	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || response.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", response.Code, response.Body.String())
	}
}

func TestMainDelegatesToAPIServerFactory(t *testing.T) {
	requireMainCallsFactory(t, "newAPIServer")
}

func requireServerTimeouts(t *testing.T, server *http.Server) {
	t.Helper()
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 15*time.Second || server.WriteTimeout != 30*time.Second || server.IdleTimeout != 60*time.Second {
		t.Fatalf("timeouts = header:%s read:%s write:%s idle:%s", server.ReadHeaderTimeout, server.ReadTimeout, server.WriteTimeout, server.IdleTimeout)
	}
}

func requireMainCallsFactory(t *testing.T, factory string) {
	t.Helper()
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
			if ok && identifier.Name == factory {
				called = true
			}
			return true
		})
	}
	if !called {
		t.Fatalf("main must delegate to %s instead of calling a server directly", factory)
	}
}
