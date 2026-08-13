package main

import (
	"context"
	"log"
	"net/http"

	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		log.Fatal(err)
	}

	svc := commerce.NewService(commerce.NewPostgresStore(pool))
	handler := newMCPHandler(mcpserver.New(svc), cfg.MCPGatewayToken)
	log.Printf("commerce MCP listening on %s/mcp", cfg.MCPAddr)
	log.Fatal(http.ListenAndServe(cfg.MCPAddr, handler))
}

func newMCPHandler(server *mcp.Server, token string) http.Handler {
	transport := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	return newMCPMux(token, transport)
}

func newMCPMux(token string, transport http.Handler) http.Handler {
	limitedTransport := http.MaxBytesHandler(transport, 1<<20)
	handler := mcpserver.TrustedContextMiddleware(token, limitedTransport)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	return mux
}
