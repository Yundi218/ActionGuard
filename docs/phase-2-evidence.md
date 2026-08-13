# Phase 2 Evidence

This document maps the implemented Phase 2 behavior to reproducible checks. The default providers are deterministic fixture implementations; no model-quality, latency, cost, or production claims are made.

| Claim | Evidence |
|---|---|
| Migrations persist policy, session, run, and plan state. | `TEST_DATABASE_URL="$DATABASE_URL" go test -p 1 ./internal/database ./internal/planningstore -count=1` |
| Embedded public policies import idempotently with deterministic 1536-wide embeddings. | `TEST_DATABASE_URL="$DATABASE_URL" go test -p 1 ./internal/policy ./cmd/policy-import -count=1` |
| Retrieval filters policy metadata and time before deterministic hybrid ranking and returns stable citations. | `TEST_DATABASE_URL="$DATABASE_URL" go test -p 1 ./internal/policy -run '^TestRetriever' -count=1` |
| Planner output is decoded into typed plans and verifier blocks malformed, unauthorized, cross-user, and policy-free actions. | `go test -p 1 ./internal/agent ./internal/verifier -count=1` |
| High-risk plans stop at `waiting_runtime` before any MCP tool call. | `go test -p 1 ./internal/tools -count=1` |
| The Session API binds identity to bearer credentials and persists retrieval, plan, verification, and bounded execution results. | `go test -p 1 ./internal/httpapi ./internal/orchestrator -count=1` |
| The replacement path uses real PostgreSQL, policy storage, streamable HTTP MCP, trusted context, and the Session API. It proves electronics resolution, filtered/cited evidence, valid typed plan and verifier result, owned replacement-only mutation, Scope and ownership rejection, high-risk no-call behavior, and shipment-text containment. | `TEST_DATABASE_URL="$DATABASE_URL" go test -p 1 ./internal/orchestrator -run '^TestPolicyConstrainedReplacementFlow$' -count=1 -v` |
| Compose declares the deterministic PostgreSQL -> fixtures -> policy import -> MCP -> API dependency chain and explicit container commands. | `go test ./scripts -run 'Test(Compose|Dockerfile)' -count=1`; run `docker compose -f deploy/docker-compose.yml config` where Docker is available. |
| CI runs serial PostgreSQL-backed tests, race tests, module verification, and vet. | `.github/workflows/ci.yml` plus `go mod verify`, `TEST_DATABASE_URL="$DATABASE_URL" go test -p 1 ./... -count=1`, `TEST_DATABASE_URL="$DATABASE_URL" go test -race -p 1 ./... -count=1`, and `go vet ./...` |

## Boundaries

Phase 2 only implements policy retrieval, typed planning, deterministic verification, and bounded synchronous execution. Temporal durability, approval execution, evaluation, and UI remain roadmap work. OpenAI-compatible providers are opt-in through `LLM_PROVIDER=openai`, `EMBEDDING_PROVIDER=openai`, `OPENAI_API_KEY`, `OPENAI_MODEL`, and `OPENAI_EMBEDDING_MODEL`; no external provider call is part of deterministic verification.
