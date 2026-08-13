# Task 4 Report: Commerce Operations over MCP

## Result

Implemented the eight commerce tools with the official MCP Go SDK, trusted
HTTP context injection, exact per-tool scopes and risk classes, JSON envelope
results, replay propagation, shipment-note trust separation, and a stateless
streamable HTTP endpoint at `/mcp` with a 1 MiB request-body cap.

## TDD Evidence

### Discovery RED

Command:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./internal/mcpserver -run TestServerListsExactCommerceToolSet -v
```

Observed failure:

```text
internal/mcpserver/server_test.go:13:12: undefined: New
FAIL github.com/Yundi218/ActionGuard/internal/mcpserver [build failed]
```

After adding only registration, typed parameter definitions, and placeholder
handlers, the same command passed. The discovery test closes both the
`ServerSession` and `ClientSession`, requires exactly eight unique tool names,
checks each risk prefix, and rejects trusted metadata properties in every input
schema.

### Handler RED

Command:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./internal/mcpserver -run 'TestTrustedContextMiddleware|TestEveryHandler|TestReadHandlers|TestHandlerRejects|TestHandlersForward|TestCreateReturnReplay|TestShipmentNote' -v
```

Observed failure before middleware and handler implementation:

```text
internal/mcpserver/server_test.go:280:13: undefined: TrustedContextMiddleware
internal/mcpserver/server_test.go:310:13: undefined: TrustedContextMiddleware
FAIL github.com/Yundi218/ActionGuard/internal/mcpserver [build failed]
```

After implementation, the same command passed all middleware, scope, risk,
metadata, forwarding, replay, and untrusted-text subtests for all eight tools.

### HTTP Body-Cap RED

Command:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./cmd/commerce-mcp -run TestMCPHandlerCapsRequestBodyAtOneMiB -v
```

Observed failure:

```text
cmd/commerce-mcp/main_test.go:25:13: undefined: newMCPMux
FAIL github.com/Yundi218/ActionGuard/cmd/commerce-mcp [build failed]
```

After wiring `http.MaxBytesHandler(transport, 1<<20)`, the test passed for an
exactly 1 MiB body and rejected a 1 MiB plus one byte body.

## Verification

Formatting:

```text
gofmt -w cmd/commerce-mcp internal/mcpserver
```

Focused MCP tests:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./internal/mcpserver -v
```

Result: PASS. This covered the exact discovery set, both authentication paths,
trusted metadata validation, all eight scope/risk classifications, all eight
argument-forwarding paths, replay envelopes, and shipment-note separation.

The focused HTTP cap test also passed with the command recorded in the HTTP
Body-Cap RED section.

Full suite:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./...
```

Result: PASS for `cmd/commerce-mcp`, `internal/commerce`, `internal/config`,
`internal/database`, `internal/httpapi`, `internal/mcpserver`, and
`internal/toolkit`; packages without tests were reported normally.

An additional uncached run also passed:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test -count=1 ./...
```

Static analysis:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go vet ./...
```

Result: exit code 0 with no output.

## Dependencies

- `github.com/modelcontextprotocol/go-sdk v1.4.0` is a direct requirement.
- The project `go` directive remains `1.24.0`.
- The SDK v1.4.0 module also declares `go 1.24.0`.
- Verification used the installed `go1.26.4` toolchain against the Go 1.24
  module contract.
- `go mod tidy` recorded the official SDK's required transitive modules in
  `go.mod` and `go.sum`.

The controller supplied the initial official dependency with:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go get github.com/modelcontextprotocol/go-sdk@v1.4.0
```

The first local dependency attempt through the configured
`https://goproxy.bytedance.com,direct` failed because the proxy hostname did not
resolve. An initial sandboxed public-proxy `go mod tidy` also failed DNS lookup;
the approved unsandboxed retry using the controller-provided environment
succeeded. No alternate dependency source or replacement directive was used.

## Files

- `.superpowers/sdd/task-4-report.md`
- `Makefile`
- `go.mod`
- `go.sum`
- `cmd/commerce-mcp/main.go`
- `cmd/commerce-mcp/main_test.go`
- `internal/mcpserver/handlers.go`
- `internal/mcpserver/server.go`
- `internal/mcpserver/server_test.go`

No commerce service rules or Postgres store files were changed.

## Deviations

- Added `cmd/commerce-mcp/main_test.go` and the unexported `newMCPHandler` /
  `newMCPMux` constructors so the required 1 MiB endpoint cap is exercised by
  a behavior test rather than source-text matching.
- Used a shared unexported `validateCall` helper to apply the same ordered
  context, risk, and scope checks in every handler. Behavior and exact scopes
  remain those specified by the brief.
