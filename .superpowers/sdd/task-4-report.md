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

## Review Follow-up: Transport and Tool Contracts (2026-08-13)

### Findings and Fixes

- `http.MaxBytesHandler` alone delegated an over-limit read to the MCP SDK,
  whose transport maps `*http.MaxBytesError` to HTTP 400. The production chain
  now authenticates first, then reads at most 1 MiB plus one byte into a fixed
  buffer. It returns HTTP 413 itself for an oversized request, including a
  request with unknown/chunked content length, replaces an in-limit body with a
  fresh readable stream, and retains `http.MaxBytesHandler` around the SDK as
  defense in depth.
- Tool registration and handler validation previously repeated names,
  descriptions, risks, and scopes independently. Each of the eight tools now
  has one `toolContract`; registration consumes its name and description, and
  the corresponding handler passes that same contract to validation. This pins
  `issue_refund` and `issue_coupon` to `toolkit.HighRiskWrite`.
- MCP discovery now compares the complete description string for every tool,
  not only the risk prefix.

### RED

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./cmd/commerce-mcp -run 'TestMCPHandlerRejectsAuthenticatedOversizedRequestBeforeMCPParsing|TestMCPMuxPassesExactlyOneMiBBodyToDownstream|TestMCPHandlerRejectsUnauthenticatedOversizedRequestWithoutReadingBody' -v
```

Observed before the limiter change:

```text
TestMCPHandlerRejectsAuthenticatedOversizedRequestBeforeMCPParsing
status = 400, want 413
```

The new contract test also failed before the contract implementation because
`toolContract`, the eight named contracts, and `toolContracts` were undefined.

### GREEN

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./cmd/commerce-mcp ./internal/mcpserver -v
```

Result: PASS. The HTTP tests cover a real `newMCPHandler(mcpserver.New(nil),
token)` request with an authenticated, unknown-length oversized body returning
413 before MCP parsing; an exactly 1 MiB body restored to a recording
downstream; and an unauthenticated oversized request returning 401 with zero
body bytes consumed. MCP tests assert all eight exact
name/description/risk/scope contracts, exact discovery descriptions, and the
per-handler contract scopes.

### Final Verification

```text
gofmt -w cmd/commerce-mcp internal/mcpserver
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./...
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go vet ./...
git diff --check
```

Result: all commands exited successfully. The full test suite passed for all
packages with tests; `cmd/api` and `migrations` reported no test files.

## Second Review Follow-up: Authoritative Catalog and Bounded Reads (2026-08-13)

### Catalog RED

The review test was changed first to iterate `commerceToolCatalog`, compare the
exact embedded contract on every entry, require a registration adapter, and
pass each iterated entry's contract into its typed handler factory.

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./internal/mcpserver -run 'TestCommerceToolCatalogContractsAreExact|TestEveryHandlerRequiresItsExactScope|TestReadHandlersDoNotRequireIdempotencyAndWritesDo' -count=1
```

Observed before the catalog refactor:

```text
internal/mcpserver/server_test.go:160:42: undefined: commerceToolCatalog
internal/mcpserver/server_test.go:167:44: too many arguments in call to getOrderHandler
internal/mcpserver/server_test.go:202:47: too many errors
FAIL github.com/Yundi218/ActionGuard/internal/mcpserver [build failed]
```

After implementation, the focused catalog, discovery, scope, risk,
argument-forwarding, replay, and trust-separation tests all passed. `New`
iterates the single catalog; each entry's generic registration adapter keeps
the MCP SDK handler parameter type and passes that entry's contract into the
handler factory and then `validateCall`.

The exact test was also mutation-checked by changing only the production
`issue_refund` catalog risk from `HighRiskWrite` to `Write` while retaining
`refund:write`:

```text
catalog contract 6 = mcpserver.toolContract{Name:"issue_refund", Description:"[high_risk_write] Issue an idempotent refund after approval", Risk:"write", Scope:"refund:write"}, want mcpserver.toolContract{Name:"issue_refund", Description:"[high_risk_write] Issue an idempotent refund after approval", Risk:"high_risk_write", Scope:"refund:write"}
--- FAIL: TestCommerceToolCatalogContractsAreExact
```

Restoring `HighRiskWrite` made the exact test pass. Handler tests construct
their invocations from those same catalog entries, so the contract supplied to
handler validation changes with the production entry; there is no separate
per-handler contract global.

### Request Body RED

The production-handler tests cover both known `Content-Length` and
unknown/chunked oversized requests returning HTTP 413. The exact-limit test
also checks body replacement and original-body closure, while the 401 test
retains its zero-byte-read assertion. A small-body read-size test exposed the
eager allocation:

```text
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test ./cmd/commerce-mcp -run 'TestMCPHandlerReturns413ForKnownContentLength|TestMCPHandlerReturns413ForUnknownChunkedLength|TestMCPMuxPassesExactlyOneMiBBodyToDownstream|TestLimitRequestBodyDoesNotPreallocateMaximumBuffer|TestMCPHandlerRejectsUnauthenticatedOversizedRequestWithoutReadingBody' -count=1 -v
```

Observed before replacing the fixed allocation:

```text
=== RUN   TestLimitRequestBodyDoesNotPreallocateMaximumBuffer
    main_test.go:126: largest read buffer = 1048577, want dynamic buffer smaller than 1048576
--- FAIL: TestLimitRequestBodyDoesNotPreallocateMaximumBuffer (0.00s)
```

### GREEN

`limitRequestBody` now uses `io.LimitReader` capped at `max+1` and
`io.ReadAll`'s dynamic buffer, rejects `len(body) > max`, restores accepted
bodies, and closes the original body. Authentication remains outside the body
limiter, and `http.MaxBytesHandler` remains around the MCP transport as defense
in depth. The same focused command passed all five tests.

### Final Verification

```text
gofmt -w cmd/commerce-mcp/main.go cmd/commerce-mcp/main_test.go internal/mcpserver/server.go internal/mcpserver/handlers.go internal/mcpserver/server_test.go
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test -count=1 ./cmd/commerce-mcp ./internal/mcpserver
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go test -count=1 ./...
GOCACHE=/private/tmp/actionguard-gocache GOPATH=/private/tmp/actionguard-gopath GOMODCACHE=/private/tmp/actionguard-gomodcache GOPROXY=https://proxy.golang.org,direct go vet ./...
git diff --check
```

All commands exited 0. The focused packages passed; the full suite passed for
all packages with tests; `cmd/api` and `migrations` reported no test files;
`go vet` and `git diff --check` produced no findings.
