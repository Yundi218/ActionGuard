package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Yundi218/ActionGuard/internal/auth"
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/httpapi"
	"github.com/Yundi218/ActionGuard/internal/httpserver"
	"github.com/Yundi218/ActionGuard/internal/llm"
	"github.com/Yundi218/ActionGuard/internal/orchestrator"
	"github.com/Yundi218/ActionGuard/internal/planningstore"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/querycontext"
	"github.com/Yundi218/ActionGuard/internal/tools"
	"github.com/Yundi218/ActionGuard/internal/verifier"
)

var (
	errStartup = errors.New("api startup failed")
	errServing = errors.New("api server failed")
)

type serverFactory func(string, ...httpapi.Dependencies) *http.Server

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	factory := func(addr string, dependencies ...httpapi.Dependencies) *http.Server {
		return newAPIServer(addr, dependencies...)
	}
	if err := runApplication(ctx, cfg, factory); err != nil {
		log.Fatal(err)
	}
}

func runApplication(ctx context.Context, cfg config.Config, newServer serverFactory) error {
	dependencies, closeRuntime, err := buildDependencies(ctx, cfg)
	if err != nil {
		return errStartup
	}
	defer closeRuntime()
	server := newServer(cfg.APIAddr, dependencies)
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return errServing
		}
		err := <-serverErrors
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errServing
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errServing
	}
}

func buildDependencies(ctx context.Context, cfg config.Config) (httpapi.Dependencies, func(), error) {
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	closePool := func() { pool.Close() }
	if err := database.Migrate(ctx, pool); err != nil {
		closePool()
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	embedder, err := newEmbedder(cfg.ProviderSettings)
	if err != nil {
		closePool()
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	planner, err := newPlanner(cfg.ProviderSettings)
	if err != nil {
		closePool()
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	commerceService := commerce.NewService(commerce.NewPostgresStore(pool))
	mcpExecutor, err := tools.NewMCPExecutor(ctx, tools.MCPConfig{Endpoint: cfg.MCPURL, GatewayToken: cfg.MCPGatewayToken, HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	if err != nil {
		closePool()
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	closeAll := func() { _ = mcpExecutor.Close(); pool.Close() }
	service, err := orchestrator.New(orchestrator.Dependencies{Store: planningstore.NewPostgresStore(pool), Resolver: querycontext.NewResolver(commerceService), Retriever: policy.NewRetriever(pool, embedder), Planner: planner, Verifier: verifier.New(commerceService), Executor: mcpExecutor})
	if err != nil {
		closeAll()
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	authenticator, err := newDemoAuthenticator(cfg)
	if err != nil {
		closeAll()
		return httpapi.Dependencies{}, func() {}, errStartup
	}
	return httpapi.Dependencies{Authenticator: authenticator, Sessions: service}, closeAll, nil
}

func newEmbedder(settings config.ProviderSettings) (policy.Embedder, error) {
	switch settings.EmbeddingProvider {
	case config.ProviderDeterministic:
		return policy.DeterministicEmbedder{}, nil
	case config.ProviderOpenAI:
		return llm.NewOpenAIEmbedder(llm.OpenAIEmbeddingConfig{BaseURL: settings.OpenAIBaseURL, APIKey: settings.OpenAIAPIKey, Model: settings.OpenAIEmbeddingModel})
	default:
		return nil, errStartup
	}
}

func newPlanner(settings config.ProviderSettings) (llm.Planner, error) {
	switch settings.LLMProvider {
	case config.ProviderDeterministic:
		return llm.NewFixturePlanner(), nil
	case config.ProviderOpenAI:
		return llm.NewOpenAIPlanner(llm.OpenAIPlannerConfig{BaseURL: settings.OpenAIBaseURL, APIKey: settings.OpenAIAPIKey, Model: settings.OpenAIModel})
	default:
		return nil, errStartup
	}
}

func newDemoAuthenticator(cfg config.Config) (*auth.Static, error) {
	fullScopes := []string{"order:read", "shipment:read", "inventory:read", "eligibility:read", "return:write", "replacement:write", "refund:write", "coupon:write"}
	readScopes := []string{"order:read", "shipment:read", "inventory:read", "eligibility:read"}
	return auth.NewStatic([]auth.Credential{
		{Token: cfg.DemoFullToken, Principal: auth.Principal{UserID: "user_018", Scopes: fullScopes}},
		{Token: cfg.DemoReadOnlyToken, Principal: auth.Principal{UserID: "user_018", Scopes: readScopes}},
		{Token: cfg.DemoUser999Token, Principal: auth.Principal{UserID: "user_999", Scopes: fullScopes}},
	})
}

func newAPIServer(addr string, dependencies ...httpapi.Dependencies) *http.Server {
	configured := httpapi.Dependencies{}
	if len(dependencies) > 0 {
		configured = dependencies[0]
	}
	return httpserver.New(addr, httpapi.NewRouter(configured))
}
