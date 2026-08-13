package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/llm"
	"github.com/Yundi218/ActionGuard/internal/policy"
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

func TestDemoAuthenticatorUsesFixedCanonicalAuthorization(t *testing.T) {
	cfg := config.Config{DemoFullToken: "full", DemoReadOnlyToken: "read", DemoUser999Token: "other"}
	authenticator, err := newDemoAuthenticator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		token, user string
		scopes      int
	}{{"full", "user_018", 8}, {"read", "user_018", 4}, {"other", "user_999", 8}}
	for _, test := range tests {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+test.token)
		principal, err := authenticator.Authenticate(req)
		if err != nil {
			t.Fatal(err)
		}
		if principal.UserID != test.user || len(principal.Scopes) != test.scopes {
			t.Fatalf("principal=%#v", principal)
		}
	}
}

func TestProviderCompositionIsExplicitAndOfflineByDefault(t *testing.T) {
	settings := config.ProviderSettings{LLMProvider: config.ProviderDeterministic, EmbeddingProvider: config.ProviderDeterministic}
	planner, err := newPlanner(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := planner.(*llm.FixturePlanner); !ok {
		t.Fatalf("planner=%T", planner)
	}
	embedder, err := newEmbedder(settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := embedder.(policy.DeterministicEmbedder); !ok {
		t.Fatalf("embedder=%T", embedder)
	}
	if _, err := newPlanner(config.ProviderSettings{LLMProvider: "unknown"}); err == nil {
		t.Fatal("unknown planner accepted")
	}
	if _, err := newEmbedder(config.ProviderSettings{EmbeddingProvider: "unknown"}); err == nil {
		t.Fatal("unknown embedder accepted")
	}
	_, _ = planner.Plan(context.Background(), llm.PlanRequest{})
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
