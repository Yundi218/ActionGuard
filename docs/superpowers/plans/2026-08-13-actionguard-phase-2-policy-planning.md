# ActionGuard Phase 2 Policy And Planning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a reproducible Agent planning layer that imports versioned policies, performs filtered PostgreSQL hybrid retrieval, generates schema-constrained action plans through a replaceable LLM provider, verifies them deterministically, and executes read-only or one non-high-risk write plan through the existing MCP gateway.

**Architecture:** PostgreSQL remains the source of policy, session, run, and plan state. `internal/policy` owns Markdown ingestion, embeddings, full-text/vector retrieval, metadata filtering, citation mapping, and RRF. `internal/agent` owns the typed plan and provider-neutral planning contract, `internal/verifier` validates every plan against the stable tool registry and current business facts, and `internal/orchestrator` coordinates retrieval, one repair attempt, persistence, and a deliberately bounded synchronous executor. Phase 2 does not claim Temporal durability, asynchronous approval, compensation, SSE replay, or production model reliability.

**Tech Stack:** Go 1.24, PostgreSQL 16, pgvector, pgx v5, official MCP Go SDK v1.4.0, OpenAI Responses and Embeddings HTTP APIs behind local interfaces, `go:embed`, `net/http`, and deterministic fake providers for CI.

## Global Constraints

- Preserve every Phase 1 tool name, JSON contract, Scope, risk, trusted-context header, idempotency, and error-sanitization guarantee.
- Public fixtures and policies are synthetic and contain no employer data, credentials, endpoints, or business rules.
- Policy evidence carries `policy_id`, `version`, `section`, `start_offset`, and `end_offset`; policy claims without evidence are rejected.
- Numeric policy limits used by the verifier come from allowlisted typed front-matter fields such as `max_coupon_cents`; the verifier never parses a limit from prose.
- Retrieval uses PostgreSQL full-text search plus pgvector cosine search, metadata/effective-time filtering before ranking, Reciprocal Rank Fusion with `k=60`, and at most five final chunks.
- Embedding width is exactly 1536. Tests and the default local demo use a deterministic local embedder; `LLM_PROVIDER=openai` uses explicitly configured OpenAI-compatible endpoints and never silently falls back.
- Planner output is strict `ActionPlan` JSON. It cannot contain code, SQL, shell commands, unregistered tools, or caller-controlled identity, Scope, run/step metadata, or idempotency keys.
- The verifier checks JSON/tool schemas, a finite acyclic dependency graph, unique step IDs, structural minimality against an allowlisted prerequisite graph, exact risks, required Scopes, order ownership, exact retrieved citations, policy applicability, write postconditions, refund balance, coupon policy cap, and high-risk approval intent. It does not claim to prove semantic alignment with arbitrary natural-language intent.
- A failed plan may be repaired once using structured verifier errors. A second failure ends the run without executing any tool.
- Phase 2 may synchronously execute plans containing only reads, or plans with no high-risk step and at most one `write` operation. The entire plan is preflighted before the first MCP call. Any `high_risk_write` makes the whole run stop as `waiting_runtime` with zero tool calls; more than one ordinary write is rejected with zero tool calls.
- Every executed write derives its idempotency key from `run_id + step_id + tool_name`; the model cannot provide or override it.
- CI must remain independent of paid models and external network calls.
- Use TDD for every behavior change: observe the focused test fail for the expected reason before production code, then make it pass and run the package regression suite.
- Each task is independently reviewed and committed before the next task begins.

---

### Task 1: Phase 2 Persistence Boundary

**Files:**
- Create: `migrations/002_policy_planning.sql`
- Create: `internal/planningstore/model.go`
- Create: `internal/planningstore/store.go`
- Create: `internal/planningstore/postgres.go`
- Create: `internal/planningstore/postgres_test.go`
- Modify: `internal/database/migrate.go`
- Modify: `internal/database/migrate_test.go`

**Interfaces:**
- Produces: `planningstore.Store` with user-bound session access, atomic planning snapshots, compare-and-set run transitions, and complete sanitized run views.
- Produces: PostgreSQL tables `policy_documents`, `policy_chunks`, `sessions`, `messages`, `runs`, and `plans`.
- Consumes: the existing embedded migration runner and `pgxpool.Pool`.

- [ ] **Step 1: Write failing migration and store tests**

Add tests proving the migration runner executes every embedded `NNN_*.sql` file in lexical order, migration `002` installs `vector`, creates all six tables, adds non-null `products.category` with a safe `general` default, upgrades a database containing only schema `001`, and is idempotent. Enforce unique `(policy_id, version)`, store a `vector(1536)`, bind a session to one user, and persist a run plus message atomically. Plan, evidence, verification, and sanitized execution result payloads are `jsonb`; run status is constrained to `planning`, `needs_input`, `ready`, `running`, `waiting_runtime`, `succeeded`, and `failed`.

- [ ] **Step 2: Run the focused tests and confirm RED**

Run:

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/database ./internal/planningstore -count=1 -v
```

Expected: failure because migration `002` and `planningstore` do not exist.

- [ ] **Step 3: Implement the migration and store**

Use these public contracts:

```go
type Session struct {
    ID        string
    UserID    string
    Region    string
    CreatedAt time.Time
}

type Run struct {
    ID           string
    SessionID    string
    Status       string
    Goal         string
    FailureCode  string
    FailureDetail string
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type PlanningSnapshot struct {
    Goal         string
    PlanVersion  int
    Plan         json.RawMessage
    Evidence     json.RawMessage
    Verification json.RawMessage
}

type RunView struct {
    Run
    PlanVersion  int
    Plan         json.RawMessage
    Evidence     json.RawMessage
    Verification json.RawMessage
    Result       json.RawMessage
}

type Store interface {
    CreateSession(context.Context, Session) error
    GetSession(context.Context, string, string) (Session, error) // sessionID, userID
    CreateRun(context.Context, Run, string) error                // userMessage
    SavePlanningSnapshot(context.Context, string, string, string, PlanningSnapshot) error // runID, from, to
    TransitionRun(context.Context, string, string, string, json.RawMessage, string, string) error
    GetRunView(context.Context, string, string) (RunView, error) // runID, userID
}
```

`CreateRun` stores the user message in the same transaction. `SavePlanningSnapshot` inserts the versioned plan and atomically compares/transitions run state while updating the goal. `TransitionRun` uses `UPDATE ... WHERE status = $from`, stores only a sanitized `result_json`, and reports a stable state-conflict error when no row changes. `GetRunView` joins session ownership and returns the latest plan snapshot; a different user receives the same stable not-found response as a missing run. Map database failures to stable domain errors without leaking SQL text.

- [ ] **Step 4: Run focused and migration regressions**

Run the command from Step 2. Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add migrations/002_policy_planning.sql internal/planningstore internal/database/migrate.go internal/database/migrate_test.go
git commit -m "feat: add policy planning persistence"
```

---

### Task 2: Versioned Policy Ingestion

**Files:**
- Create: `internal/policy/model.go`
- Create: `internal/policy/parser.go`
- Create: `internal/policy/parser_test.go`
- Create: `internal/policy/embedder.go`
- Create: `internal/policy/deterministic_embedder.go`
- Create: `internal/policy/deterministic_embedder_test.go`
- Create: `internal/policy/importer.go`
- Create: `internal/policy/importer_test.go`
- Create: `internal/policy/postgres_store.go`
- Create: `internal/policyassets/embed.go`
- Create: `internal/policyassets/embed_test.go`
- Create: `policies/damaged-goods-v3.md`
- Create: `policies/refunds-v2.md`
- Create: `policies/customer-care-v1.md`

**Interfaces:**
- Produces: `policy.ParseMarkdown`, `policy.Embedder`, `policy.DeterministicEmbedder`, `policy.Importer`, and policy repository writes.
- Consumes: `policy_documents` and `policy_chunks` from Task 1.

- [ ] **Step 1: Write failing parser, embedding, and importer tests**

Fixtures use YAML-like front matter with exact required keys:

```markdown
---
policy_id: damaged_goods
version: v3
effective_from: 2026-01-01T00:00:00Z
effective_to: 2027-01-01T00:00:00Z
region: CN
product_category: electronics
risk_level: write
max_coupon_cents: 2000
---
# Damaged goods
## Replacement window
...
```

Tests prove missing/unknown metadata is rejected, timestamps normalize to UTC, applicability is the half-open interval `[effective_from, effective_to)`, `effective_from < effective_to`, headings define stable sections, chunks never cross a section, chunks retain byte offsets into the original body, repeated deterministic embedding is identical and normalized, width is 1536, and importing the same `(policy_id, version, content_sha256)` is idempotent while changed content replaces chunks transactionally. `ChunkID` is lowercase SHA-256 hex over `policy_id + NUL + version + NUL + section + NUL + decimal(start) + NUL + decimal(end) + NUL + content_sha256`.

- [ ] **Step 2: Run tests and confirm RED**

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/policy -run 'Test(Parse|Deterministic|Import)' -count=1 -v
```

Expected: package or symbols missing.

- [ ] **Step 3: Implement the types and ingestion pipeline**

Use these contracts:

```go
const EmbeddingDimensions = 1536

type Metadata struct {
    PolicyID       string
    Version        string
    EffectiveFrom  time.Time
    EffectiveTo    time.Time
    Region         string
    ProductCategory string
    RiskLevel      toolkit.Risk
    MaxCouponCents *int64
}

type Chunk struct {
    ID          string
    Metadata    Metadata
    Section     string
    Text        string
    StartOffset int
    EndOffset   int
    Embedding   []float32
}

type Embedder interface {
    Embed(context.Context, []string) ([][]float32, error)
}

type Importer struct { /* parser, embedder, store */ }
func (i *Importer) Import(ctx context.Context, sourceName string, markdown []byte) error
```

Chunk by Markdown `##` sections and then deterministic paragraph groups capped at 1200 bytes with 150-byte overlap. Preserve UTF-8 boundaries. `DeterministicEmbedder` tokenizes lowercase alphanumeric terms, hashes each token into a dimension and sign, then L2 normalizes; it is a reproducible test/demo baseline, not a semantic-model claim.

`internal/policyassets` uses `go:embed` to package exactly the three public policies and exposes them in lexical filename order. The import command and container demo consume these embedded bytes; no runtime policy volume is required.

- [ ] **Step 4: Add the three synthetic policies and pass tests**

The policies must define the 30-day delivered-order replacement window, refundable-balance limit, damaged-electronics replacement eligibility, and coupon cap of 2000 cents. Run:

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/policy -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/policy internal/policyassets policies
git commit -m "feat: import versioned policy evidence"
```

---

### Task 3: Filtered Hybrid Retrieval And Citations

**Files:**
- Create: `internal/policy/retriever.go`
- Create: `internal/policy/retriever_test.go`
- Modify: `internal/policy/postgres_store.go`
- Create: `cmd/policy-import/main.go`
- Create: `cmd/policy-import/main_test.go`
- Modify: `Makefile`

**Interfaces:**
- Produces: `policy.Retriever.Search(context.Context, policy.Query) ([]policy.Evidence, error)`.
- Produces: `make policies` for importing all embedded public Markdown files.
- Consumes: Task 2 chunks and embedder.

- [ ] **Step 1: Write failing retrieval tests**

Use this query/result contract:

```go
type Query struct {
    Text            string
    At              time.Time
    Region          string
    ProductCategory string
    RiskLevels      []toolkit.Risk
    Limit           int
}

type Evidence struct {
    ChunkID        string       `json:"chunk_id"`
    CitationID     string       `json:"citation_id"`
    PolicyID       string       `json:"policy_id"`
    Version        string       `json:"version"`
    Section        string       `json:"section"`
    StartOffset    int          `json:"start_offset"`
    EndOffset      int          `json:"end_offset"`
    EffectiveFrom  time.Time    `json:"effective_from"`
    EffectiveTo    time.Time    `json:"effective_to"`
    Region          string       `json:"region"`
    ProductCategory string       `json:"product_category"`
    RiskLevel       toolkit.Risk `json:"risk_level"`
    MaxCouponCents *int64       `json:"max_coupon_cents,omitempty"`
    Text            string       `json:"text"`
    Score           float64      `json:"score"`
}
```

`CitationID` is `policy_id:version:section:start_offset-end_offset`. `Text`, `Region`, and `ProductCategory` must be non-empty; an empty `RiskLevels` slice means all risk levels. Tests prove filters are applied inside both lexical and vector SQL before `LIMIT`, `[effective_from, effective_to)` boundaries are exact, expired/wrong-region/wrong-category policies never appear, RRF uses `1/(60+rank)`, duplicate chunks merge, ties use stable `chunk_id`, `Limit == 0` defaults to 5, limits above 5 clamp to 5, negative limits fail, offsets map exactly to source text, and a fixed damaged-headphones query retrieves the replacement and coupon policies in the expected top five.

- [ ] **Step 2: Run tests and confirm RED**

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/policy -run 'TestRetriever' -count=1 -v
```

- [ ] **Step 3: Implement retrieval**

Run two parameterized SQL queries with candidate limit 20: `websearch_to_tsquery('english', $query)` against a stored generated `tsvector`, and cosine distance `embedding <=> $vector`. RRF fusion happens in Go over one-based ranks with `k=60`; final sort is descending fused score then ascending chunk ID. Never interpolate metadata or query text into SQL.

- [ ] **Step 4: Write command tests and confirm RED**

Test deterministic import of all embedded assets, stable filename order, missing database configuration, and sanitized output. Run:

```bash
go test ./cmd/policy-import -count=1 -v
```

Expected: failure because the import command does not exist.

- [ ] **Step 5: Implement the import command**

`cmd/policy-import` opens PostgreSQL, calls `database.Migrate` before any policy write, then reads the embedded public assets in lexical order and uses the deterministic embedder in this task. Its tests start from an empty database and prove migration `002` exists before import. It reports policy count only, not full policy content or credentials. Task 6 extends its explicit provider factory with OpenAI embeddings.

- [ ] **Step 6: Run policy and command regressions**

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/policy ./cmd/policy-import -count=1
```

- [ ] **Step 7: Commit**

```bash
git add internal/policy cmd/policy-import Makefile
git commit -m "feat: add filtered hybrid policy retrieval"
```

---

### Task 4: Trusted Retrieval Context Resolution

**Files:**
- Modify: `internal/commerce/model.go`
- Modify: `internal/commerce/store.go`
- Modify: `internal/commerce/postgres_store.go`
- Modify: `internal/commerce/postgres_store_test.go`
- Modify: `internal/commerce/service.go`
- Modify: `internal/commerce/service_test.go`
- Modify: `fixtures/commerce.sql`
- Create: `internal/querycontext/resolver.go`
- Create: `internal/querycontext/resolver_test.go`

**Interfaces:**
- Produces: an ownership-checked `commerce.OrderContext` and deterministic `querycontext.Resolver`.
- Consumes: the user-bound session region, raw user message, a clock, and Task 1 product categories.

- [ ] **Step 1: Write failing commerce fact tests**

Add `Store.GetProductCategory(context.Context, sku string) (string, error)` and `Service.ResolveOrderContext(context.Context, userID, orderID string) (OrderContext, error)`. Tests prove ownership is checked before category lookup, `AG-1042` resolves to `electronics`, missing products use stable errors, and no new field appears in any Phase 1 MCP JSON result.

- [ ] **Step 2: Run commerce tests and confirm RED**

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/commerce ./internal/mcpserver -run 'Test.*(OrderContext|ProductCategory|ToolCatalog)' -count=1 -v
```

Expected: missing store/service contracts.

- [ ] **Step 3: Implement the ownership-checked fact method**

Use:

```go
type OrderContext struct {
    Order           Order
    ProductCategory string
}
```

This is an internal planning fact and is not a ninth MCP tool. Update only synthetic fixture product categories.

- [ ] **Step 4: Write resolver tests and confirm RED**

Use:

```go
type Context struct {
    OrderID        string
    Region         string
    ProductCategory string
    At             time.Time
}

type OrderContextReader interface {
    ResolveOrderContext(context.Context, string, string) (commerce.OrderContext, error)
}

func (r *Resolver) Resolve(context.Context, string, string, string) (Context, error) // userID, region, message
```

Tests prove the case-sensitive boundary pattern `\bAG-[0-9]{4,}\b`, exactly one distinct order ID, UTC clock normalization, session-derived region, ownership enforcement, and product-category lookup. No order ID returns stable `ErrNeedsInput`; multiple distinct IDs return stable `ErrAmbiguousOrder`. Neither error calls the retriever or planner.

Run:

```bash
go test ./internal/querycontext -count=1 -v
```

Expected: package missing.

- [ ] **Step 5: Implement the resolver and pass regressions**

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/commerce ./internal/querycontext ./internal/mcpserver -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/commerce internal/querycontext internal/mcpserver fixtures/commerce.sql
git commit -m "feat: resolve trusted policy query context"
```

---

### Task 5: Typed Action Plan And Deterministic Verifier

**Files:**
- Create: `internal/agent/plan.go`
- Create: `internal/agent/plan_test.go`
- Create: `internal/agent/schema.go`
- Create: `internal/agent/schema_test.go`
- Modify: `internal/toolkit/contract.go`
- Modify: `internal/toolkit/contract_test.go`
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/server_test.go`
- Create: `internal/verifier/verifier.go`
- Create: `internal/verifier/verifier_test.go`

**Interfaces:**
- Produces: canonical `agent.ActionPlan`, strict JSON Schema, centralized tool registry, and `verifier.VerifyAndSeal`.
- Consumes: `policy.Evidence`, the stable eight-tool contracts, and a `verifier.FactReader` backed by commerce state.

- [ ] **Step 1: Write failing registry and plan schema tests**

Use these plan contracts:

```go
type ActionPlan struct {
    Goal       string   `json:"goal"`
    PolicyRefs []string `json:"policy_refs"`
    Steps      []Step   `json:"steps"`
}

type Step struct {
    ID               string          `json:"id"`
    Tool             string          `json:"tool"`
    Arguments        json.RawMessage `json:"arguments"`
    DependsOn        []string        `json:"depends_on"`
    Risk             toolkit.Risk    `json:"risk"`
    SuccessCondition string          `json:"success_condition"`
    ApprovalRequired bool            `json:"approval_required"`
}
```

Tests prove strict decoding rejects unknown fields, duplicate keys, non-object arguments, more than 12 steps, unknown risks, code/SQL/shell fields, and trusted context metadata anywhere in arguments. Schema round-trips a valid replacement plan.

- [ ] **Step 2: Run schema tests and confirm RED**

```bash
go test ./internal/agent ./internal/toolkit -count=1 -v
```

- [ ] **Step 3: Centralize tool metadata and implement the plan schema**

Extend `toolkit.Contract` so the existing MCP registration and the verifier consume one immutable registry containing tool name, description, risk, Scope, resolved input schema, and whether an idempotency key is required. Keep `toolkit` as a dependency leaf: it imports neither `mcpserver` nor `verifier`; `mcpserver` consumes the exported registry. Preserve exact Phase 1 discovery output and add a regression comparing each discovered schema's canonical JSON bytes with the registry schema.

- [ ] **Step 4: Write failing verifier tests**

Cover: unknown/missing dependency, self-edge, cycle, duplicate step ID, a step outside the allowlisted prerequisite graph for the terminal tool, duplicate tool+arguments, risk mismatch, missing Scope, cross-user order, missing or non-exact citation ID, expired/wrong-region/wrong-category evidence, refund above refundable balance, coupon above typed evidence cap, write without a machine-checkable success condition, and high-risk step without `approval_required=true`. Also prove a valid damaged-item replacement plan passes and errors are returned as stable `{code, step_id, field, message}` values suitable for one repair prompt.

- [ ] **Step 5: Run verifier tests and confirm RED**

```bash
go test ./internal/verifier -count=1 -v
```

Expected: failure because `Verifier` and `VerifyAndSeal` do not exist.

- [ ] **Step 6: Implement minimal deterministic verification**

Use:

```go
type Context struct {
    UserID   string
    Scopes   []string
    Now      time.Time
    Evidence []policy.Evidence
}

type FactReader interface {
    GetOrder(context.Context, string, string) (commerce.Order, error)
}

type Result struct {
    Valid  bool    `json:"valid"`
    Errors []Error `json:"errors,omitempty"`
}

type VerifiedPlan struct { /* contains unexported ActionPlan, UserID, and canonical Scope digest fields */ }

func (v *Verifier) VerifyAndSeal(context.Context, agent.ActionPlan, Context) (VerifiedPlan, Result)
func (p VerifiedPlan) Plan() agent.ActionPlan
```

`VerifyAndSeal` sorts and deduplicates Scopes, then binds the verified `UserID` and SHA-256 digest of the NUL-delimited canonical Scope list into private `VerifiedPlan` fields. Accessors return the bound user and a defensive Scope copy for the trusted executor. Structural minimality is intentionally narrower than semantic intent understanding: the terminal tool determines an allowlisted prerequisite graph, every step must be an ancestor of that terminal, at most one terminal action is allowed in Phase 2, and exact duplicate tool+arguments pairs are rejected. The verifier does not claim to prove that the user's natural-language goal semantically requires the action. Success conditions use a small allowlist per tool, not a general expression evaluator. Every `PolicyRef` must equal a `CitationID` in `Context.Evidence`; applicability uses the evidence's frozen typed fields. Coupon cap is read from cited `customer_care` evidence metadata and is never inferred from free text.

- [ ] **Step 7: Run verifier and existing MCP contract tests**

```bash
go test ./internal/agent ./internal/verifier ./internal/toolkit ./internal/mcpserver -count=1
```

- [ ] **Step 8: Commit**

```bash
git add internal/agent internal/verifier internal/toolkit internal/mcpserver
git commit -m "feat: verify typed action plans"
```

---

### Task 6: Replaceable Structured LLM Provider

**Files:**
- Create: `internal/llm/provider.go`
- Create: `internal/llm/openai.go`
- Create: `internal/llm/openai_test.go`
- Create: `internal/llm/deterministic.go`
- Create: `internal/llm/deterministic_test.go`
- Modify: `cmd/policy-import/main.go`
- Modify: `cmd/policy-import/main_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: `llm.Planner` with initial generation and one verifier-guided repair.
- Produces: OpenAI Responses API and Embeddings API adapters plus deterministic CI/demo planner.
- Consumes: `agent.ActionPlan`, its strict schema, `policy.Evidence`, and `verifier.Error`.

- [ ] **Step 1: Write failing provider contract tests**

Use:

```go
type PlanRequest struct {
    UserMessage string
    Evidence    []policy.Evidence
    ToolContracts []toolkit.Contract
}

type RepairRequest struct {
    Original PlanRequest
    Plan     agent.ActionPlan
    Errors   []verifier.Error
}

type Planner interface {
    Plan(context.Context, PlanRequest) (agent.ActionPlan, error)
    Repair(context.Context, RepairRequest) (agent.ActionPlan, error)
}
```

An `httptest.Server` must assert that the OpenAI request uses `/v1/responses`, bearer auth, a deadline, and strict JSON Schema under `text.format`. Decode a completed response by traversing `output[]` entries of type `message`, then `content[]`; require exactly one `output_text` item and reject `refusal`, `incomplete`, zero/multiple output texts, invalid JSON, unknown plan fields, non-2xx responses, and bodies above 1 MiB with sanitized stable errors. Retry timeout, HTTP 408, 429, and 5xx at most two times after the initial attempt with injectable backoff; never retry other 4xx, invalid output, refusal, or caller cancellation. Tests assert attempt counts, cancellation during backoff, and no credential/body leakage. A second server asserts batched `/v1/embeddings` requests explicitly set `dimensions: 1536` and return exactly 1536 finite values per vector.

- [ ] **Step 2: Run tests and confirm RED**

```bash
go test ./internal/llm ./internal/config -count=1 -v
```

- [ ] **Step 3: Implement HTTP adapters and deterministic provider**

The system instruction treats evidence and user content as untrusted data, forbids identity/Scope/idempotency arguments, permits only registry tools, and asks for the minimum plan. Evidence is serialized as a separate JSON input item and explicitly labeled untrusted data, never interpolated into the instruction string. Cap response bodies while reading at 1 MiB and never log API keys or raw user content. Extend `cmd/policy-import` so `EMBEDDING_PROVIDER=openai` selects the OpenAI adapter only when all required settings are present; the default remains deterministic.

The deterministic planner supports the documented synthetic examples for CI: order lookup; damaged-item replacement; replacement plus coupon (the coupon remains `waiting_runtime`). It is labeled a fixture planner and is never described as an LLM.

- [ ] **Step 4: Run provider tests**

```bash
go test ./internal/llm ./internal/config -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/llm internal/config cmd/policy-import
git commit -m "feat: add structured planning providers"
```

---

### Task 7: Trusted MCP Plan Executor

**Files:**
- Create: `internal/tools/executor.go`
- Create: `internal/tools/mcp_executor.go`
- Create: `internal/tools/mcp_executor_test.go`
- Create: `internal/tools/metadata_transport.go`
- Modify: `internal/mcpserver/e2e_test.go`

**Interfaces:**
- Produces: `tools.Executor.Execute(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error)`.
- Consumes: `verifier.VerifiedPlan`, trusted principal/Scopes, and streamable HTTP MCP.

- [ ] **Step 1: Write failing executor tests**

Use:

```go
type ExecutionRequest struct {
    RunID string
    Plan  verifier.VerifiedPlan
}

type StepResult struct {
    StepID        string
    Tool          string
    Trusted       json.RawMessage
    UntrustedText map[string]string
    Replayed      bool
}

type ExecutionResult struct {
    Status  string
    Steps   []StepResult
}
```

Tests prove full-plan preflight happens before any MCP call, topological execution, dependency failure stops dependents, read-only chains execute, one ordinary write executes with `sha256(run_id + "\x00" + step_id + "\x00" + tool)` as the idempotency key, more than one ordinary write is rejected with zero calls, and any high-risk step returns `waiting_runtime` with zero calls. Invoke the same real `ExecutionRequest` twice and assert the second write result is an idempotent replay with one database side effect. Prove MCP trusted headers come only from the user and canonical Scopes sealed into `VerifiedPlan`; there is no execution-request field or model argument that can override them, and a plan sealed for another user is rejected by the commerce ownership boundary without exposing cross-user data. Use a real official MCP client against an `httptest` server.

- [ ] **Step 2: Run tests and confirm RED**

```bash
go test ./internal/tools -count=1 -v
```

- [ ] **Step 3: Implement the bounded executor**

Build a fresh metadata-bearing request context per step. Accept only `verifier.VerifiedPlan`, whose plan field can be populated only inside the verifier package; do not expose a public `Verified bool` that callers can forge. Wrap the MCP HTTP transport so `Content-Length > 1 MiB` is rejected and unknown/chunked bodies fail while reading byte `1 MiB + 1`, before the SDK can buffer more. Require exactly one text content item, strictly decode `toolkit.Envelope`, and keep `trusted` separate from `untrusted_text` in `StepResult`.

- [ ] **Step 4: Run executor and MCP regressions**

```bash
go test ./internal/tools ./internal/mcpserver -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/tools internal/mcpserver/e2e_test.go
git commit -m "feat: execute verified plans through MCP"
```

---

### Task 8: Authenticated Session Orchestrator And Phase 2 API

**Files:**
- Create: `internal/auth/principal.go`
- Create: `internal/auth/static.go`
- Create: `internal/auth/static_test.go`
- Create: `internal/orchestrator/service.go`
- Create: `internal/orchestrator/service_test.go`
- Create: `internal/httpapi/session_handlers.go`
- Create: `internal/httpapi/session_handlers_test.go`
- Modify: `internal/httpapi/router.go`
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/main_test.go`

**Interfaces:**
- Produces: `POST /v1/sessions`, `POST /v1/sessions/{session_id}/messages`, and `GET /v1/runs/{run_id}`.
- Produces: an API authentication boundary that derives immutable identity and Scopes from the bearer credential.
- Consumes: planning store, query-context resolver, retriever, planner, verifier, and trusted executor.

- [ ] **Step 1: Write authenticator tests and confirm RED**

Use:

```go
type Principal struct {
    UserID string
    Scopes []string
}

type Authenticator interface {
    Authenticate(*http.Request) (Principal, error)
}
```

The static Demo authenticator is configured with synthetic bearer tokens mapped to principals; it stores SHA-256 token digests rather than plaintext map keys after construction and returns a defensive copy of scopes. Tests cover two distinct users, invalid/missing bearer, constant-time digest comparison, spoofed `X-ActionGuard-User` and `X-ActionGuard-Scopes` having no effect, and insufficient Scope reaching the verifier as a rejection.

Run:

```bash
go test ./internal/auth -count=1 -v
```

Expected: package missing.

- [ ] **Step 2: Implement the authenticator**

The JSON request body never contains identity or Scopes. The API passes only `Principal` values returned by `Authenticator`; it does not parse trusted identity from arbitrary headers.

- [ ] **Step 3: Write failing orchestrator tests**

The orchestrator must: create a user-bound session; create a `planning` run; resolve trusted order/category context; return `needs_input` without retrieval when the order ID is missing; build `policy.Query{Text: message, At: resolved.At, Region: resolved.Region, ProductCategory: resolved.ProductCategory, Limit: 5}`; retrieve evidence; request a plan; persist plan version 1 plus evidence and verification atomically; verify; repair exactly once when invalid and persist version 2; compare-and-set `ready -> running` before execution; persist sanitized results; finish `succeeded`, `waiting_runtime`, or `failed`; and never call the executor after a second verifier failure. Cross-user session access must fail before context resolution, retrieval, or model calls.

- [ ] **Step 4: Run tests and confirm RED**

```bash
go test ./internal/orchestrator -count=1 -v
```

- [ ] **Step 5: Implement orchestration**

Use dependency interfaces local to `internal/orchestrator` and constructor validation. IDs are UUIDv4 strings generated from 16 `crypto/rand` bytes with RFC 4122 version/variant bits; validate canonical lowercase `8-4-4-4-12` encoding in tests. All state transitions are explicit; errors returned to HTTP use stable public codes while detailed provider/database errors remain server-side.

- [ ] **Step 6: Write failing HTTP tests and confirm RED**

Requests use per-principal bearer credentials through `Authenticator`; caller-supplied identity/scope headers are ignored. The body cannot set `user_id`, scopes, run IDs, or idempotency keys. Validate JSON content type, 1 MiB body cap, unknown fields, empty messages, region enum, ownership, status mapping, and cancellation. The message endpoint returns one complete Phase 2 JSON result; it does not claim SSE support.

Run:

```bash
go test ./internal/httpapi ./cmd/api -count=1 -v
```

Expected: routes and authentication composition are missing.

- [ ] **Step 7: Implement handlers and composition root**

Use exact payloads:

```json
{"region":"CN"}
```

```json
{"message":"Order AG-1042 arrived damaged. Replace it."}
```

The response includes `run_id`, `status`, `goal`, typed `plan`, verifier result, evidence citations, and sanitized step summaries. It never returns provider prompts, API keys, database errors, or untrusted text as trusted fields.

- [ ] **Step 8: Run API and orchestrator tests**

```bash
go test ./internal/orchestrator ./internal/httpapi ./cmd/api -count=1
```

- [ ] **Step 9: Commit**

```bash
git add internal/auth internal/orchestrator internal/httpapi cmd/api
git commit -m "feat: expose policy-constrained planning API"
```

---

### Task 9: Reproducible Phase 2 Demo, CI, And Documentation

**Files:**
- Create: `internal/orchestrator/e2e_test.go`
- Modify: `Dockerfile`
- Modify: `deploy/docker-compose.yml`
- Modify: `.env.example`
- Modify: `Makefile`
- Modify: `README.md`
- Modify: `.github/workflows/ci.yml`
- Create: `docs/phase-2-evidence.md`

**Interfaces:**
- Produces: one-command deterministic Phase 2 demo and named PostgreSQL+MCP+API E2E test.
- Consumes: all earlier tasks.

- [ ] **Step 1: Write the named E2E test and confirm RED**

`TestPolicyConstrainedReplacementFlow` must migrate a real PostgreSQL database, load commerce fixtures and embedded policies, start a real streamable HTTP MCP server, call the Session API, and assert: `electronics` is resolved from the owned order; filtered citations; valid typed plan; cross-user and insufficient-Scope rejection; replacement-only state mutation; and malicious shipment text never appears in planner instructions or trusted response fields. A separate replacement-plus-coupon request must finish `waiting_runtime` with zero MCP calls and zero replacement/coupon side effects. Same-run idempotent replay remains an executor integration assertion from Task 7 rather than pretending that reposting a message reuses a run.

Run:

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./internal/orchestrator -run '^TestPolicyConstrainedReplacementFlow$' -count=1 -v
```

- [ ] **Step 2: Complete Compose and configuration**

Change the Dockerfile into one build stage that emits `/app/api`, `/app/commerce-mcp`, and `/app/policy-import`; Compose selects each command explicitly. Add one-shot embedded policy import after the commerce fixture loader and before API readiness; `policy-import` itself calls the ordered Go migration runner, so schema `002` is guaranteed before it writes policies. Configure the API with `MCP_URL=http://commerce-mcp:8081/mcp`, distinct from the MCP server's listen setting `MCP_ADDR=:8081`. Default Compose uses deterministic embeddings/planner, fixed synthetic bearer principals, and synthetic data. OpenAI mode requires explicit `LLM_PROVIDER=openai`, `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_EMBEDDING_MODEL`, and optional `OPENAI_BASE_URL`; missing values fail startup. Tests inspect the Docker stages, dependency order, and commands; verification runs `docker compose config` plus image command smoke tests when Docker is available.

- [ ] **Step 3: Update CI and README honestly**

CI runs `go mod verify`, all PostgreSQL-backed Go tests serially, and `go vet`. README changes current status to “Phase 2 implements policy retrieval, typed planning, deterministic verification, and bounded synchronous execution; Temporal durability, approval execution, evaluation, and UI remain roadmap.” Document deterministic and OpenAI modes separately and include the exact named E2E command.

- [ ] **Step 4: Add evidence document**

`docs/phase-2-evidence.md` maps every Phase 2 claim to a test name or reproducible command. It contains no fabricated model-quality, latency, cost, or production claims.

- [ ] **Step 5: Run complete verification**

```bash
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -p 1 ./... -count=1
TEST_DATABASE_URL="$TEST_DATABASE_URL" go test -race -p 1 ./... -count=1
go vet ./...
go mod verify
```

Also build Linux binaries for `cmd/api`, `cmd/commerce-mcp`, and `cmd/policy-import`, validate Compose configuration, scan tracked files for credential-like values and employer identifiers, and confirm the worktree is clean except intended commits.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestrator/e2e_test.go Dockerfile deploy .env.example Makefile README.md .github/workflows/ci.yml docs/phase-2-evidence.md
git commit -m "test: make Phase 2 reproducible"
```

---

## Phase 2 Completion Gate

Phase 2 is complete only when all of the following evidence exists:

- PostgreSQL migrations create versioned policy/session/run/plan state and pass from an empty database.
- Three public synthetic policies import idempotently with stable citations and 1536-wide embeddings.
- Fixed retrieval tests prove pre-ranking metadata/effective-time filters, lexical/vector candidate generation, RRF `k=60`, stable ties, top-five cap, and exact source offsets.
- Strict planner decoding and the verifier reject malformed, cyclic, excessive, unauthorized, policy-free, cross-user, over-refund, over-coupon, and approval-bypassing plans.
- OpenAI adapters are tested entirely with local HTTP fakes; deterministic CI never calls an external model.
- The executor sends trusted metadata only through HTTP headers, derives write idempotency keys itself, and returns `waiting_runtime` with zero MCP calls for every plan containing a high-risk step.
- The Session API performs retrieval, one plan repair at most, verification, persistence, and bounded execution without accepting caller identity in JSON.
- The named replacement E2E scenario passes against PostgreSQL and real streamable HTTP MCP.
- Full tests, race tests, vet, module verification, Linux builds, Compose validation, and public-data hygiene checks pass.
- README clearly distinguishes implemented Phase 2 behavior from Phase 3-6 roadmap items.

After this gate, write the Phase 3 plan for Temporal durable execution, approval signals, post-condition checks, retries, and compensation against the stable Phase 2 plan and executor contracts.
