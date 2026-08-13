package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"

	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/config"
	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/httpserver"
	"github.com/Yundi218/ActionGuard/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMCPRequestBodyBytes = 1 << 20

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
	server := newCommerceMCPServer(cfg.MCPAddr, handler)
	log.Printf("commerce MCP listening on %s/mcp", cfg.MCPAddr)
	log.Fatal(server.ListenAndServe())
}

func newCommerceMCPServer(addr string, handler http.Handler) *http.Server {
	return httpserver.New(addr, handler)
}

func newMCPHandler(server *mcp.Server, token string) http.Handler {
	transport := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true},
	)
	return newMCPMux(token, transport)
}

func newMCPMux(token string, transport http.Handler) http.Handler {
	limitedTransport := http.MaxBytesHandler(transport, maxMCPRequestBodyBytes)
	handler := mcpserver.TrustedContextMiddleware(token, limitRequestBody(maxMCPRequestBodyBytes, limitedTransport))
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	return mux
}

func limitRequestBody(maxBytes int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		originalBody := r.Body
		defer originalBody.Close()
		body, err := io.ReadAll(io.LimitReader(originalBody, int64(maxBytes)+1))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(body) > maxBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
