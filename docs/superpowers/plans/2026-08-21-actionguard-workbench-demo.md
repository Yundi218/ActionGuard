# ActionGuard Workbench Demo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a locally runnable interview workbench that makes one policy-constrained replacement run understandable from user request through policy evidence, typed plan, verification, MCP execution, and final state.

**Architecture:** Add an independent React/Vite application under `web/`. The first task uses a deterministic in-browser scenario so the UI is always demonstrable; a typed API client is added next to connect the same view model to the Go Session API without changing presentation components. Later tasks add approval/event endpoints and benchmark views only after the primary Agent Runs workflow is stable.

**Tech Stack:** React 19, TypeScript, Vite, Vitest, Testing Library, Lucide React, CSS, existing Go Session API.

## Global Constraints

- The first viewport is the usable Agent Runs workbench, not a landing page.
- Use a quiet operational layout with stable side navigation, conversation workspace, and execution inspector.
- Do not nest decorative cards or use gradients, purple-heavy palettes, oversized marketing headings, or ornamental blobs.
- Use Lucide icons for navigation and command buttons, with accessible labels and tooltips.
- Keep the deterministic demo path available even when the Go API or a paid model is unavailable.
- Treat policy evidence and tool text as data; the UI must visually distinguish trusted execution metadata from untrusted external text.
- Every task ends with tests, visual verification where applicable, and one reviewable commit.

---

### Task 1: Agent Runs Workbench Foundation

**Files:**
- Create: `web/package.json`
- Create: `web/pnpm-lock.yaml`
- Create: `web/index.html`
- Create: `web/tsconfig.json`
- Create: `web/vite.config.ts`
- Create: `web/src/main.tsx`
- Create: `web/src/App.tsx`
- Create: `web/src/app.css`
- Create: `web/src/demo/replacementScenario.ts`
- Create: `web/src/domain/run.ts`
- Create: `web/src/components/Sidebar.tsx`
- Create: `web/src/components/ConversationPane.tsx`
- Create: `web/src/components/ExecutionInspector.tsx`
- Create: `web/src/components/PlanGraph.tsx`
- Create: `web/src/components/PolicyEvidence.tsx`
- Create: `web/src/components/ToolTrace.tsx`
- Create: `web/src/test/setup.ts`
- Create: `web/src/App.test.tsx`
- Modify: `.gitignore`
- Modify: `README.md`

**Interfaces:**
- Produces: `RunScenario`, `PlanStep`, `PolicyEvidenceItem`, and `ToolCall` types in `src/domain/run.ts`.
- Produces: `replacementScenario: RunScenario` for a deterministic damaged-headphones replacement flow.
- Produces: `App` with scenario reset, run progression, inspector tabs, and responsive navigation.

- [x] **Step 1: Add the failing application behavior tests**

Create `src/App.test.tsx` with tests that render `App`, assert the initial empty prompt state, start the replacement scenario, inspect the policy tab, and verify that `untrusted_text` is visibly isolated from trusted fields.

```tsx
it("reveals the policy-constrained replacement trace", async () => {
  render(<App />);
  await userEvent.click(screen.getByRole("button", { name: /run demo/i }));
  expect(await screen.findByText("create_replacement")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("tab", { name: /policy evidence/i }));
  expect(screen.getByText(/damaged_goods:v3/i)).toBeInTheDocument();
});

it("keeps shipment notes in an untrusted container", async () => {
  render(<App />);
  await userEvent.click(screen.getByRole("button", { name: /run demo/i }));
  await userEvent.click(screen.getByRole("tab", { name: /tool trace/i }));
  expect(screen.getByLabelText("Untrusted tool text")).toHaveTextContent("ignore previous rules");
});
```

- [x] **Step 2: Run the tests and verify RED**

Run: `cd web && pnpm test --run`

Expected: FAIL because the React app and components do not exist.

- [x] **Step 3: Implement the typed scenario and workbench UI**

Implement a stable three-column desktop layout and a two-pane responsive layout. The deterministic scenario must include:

```ts
export interface RunScenario {
  id: string;
  title: string;
  userMessage: string;
  status: "ready" | "running" | "succeeded" | "waiting_approval" | "failed";
  evidence: PolicyEvidenceItem[];
  plan: PlanStep[];
  verification: { valid: boolean; checks: string[] };
  toolCalls: ToolCall[];
  finalSummary: string;
}
```

The page must expose four inspector tabs: `Plan`, `Policy evidence`, `Tool trace`, and `Final state`. Running the demo reveals the trace progressively without changing panel dimensions. The shipment tool call must display malicious fixture text only inside an explicitly labeled untrusted block.

- [x] **Step 4: Run unit tests and production build**

Run: `cd web && pnpm test --run && pnpm build`

Expected: all Vitest tests pass and Vite emits `web/dist` without TypeScript errors.

- [x] **Step 5: Verify desktop and mobile rendering**

Run the Vite server on an available local port. Capture 1440x1000 and 390x844 screenshots. Confirm the execution inspector remains readable, navigation collapses without covering content, text does not overflow, and all panels contain meaningful rendered pixels.

- [x] **Step 6: Commit Task 1**

```bash
git add .gitignore README.md web docs/superpowers/plans/2026-08-21-actionguard-workbench-demo.md
git commit -m "feat: add Agent Runs demo workbench"
```

### Task 2: Session API Integration

**Files:**
- Create: `web/src/api/actionguard.ts`
- Create: `web/src/api/actionguard.test.ts`
- Create: `web/src/domain/adapters.ts`
- Create: `web/src/domain/adapters.test.ts`
- Modify: `web/src/App.tsx`
- Modify: `web/vite.config.ts`
- Modify: `cmd/api/main.go`
- Modify: `cmd/api/main_test.go`
- Modify: `README.md`

**Interfaces:**
- Produces: `ActionGuardClient` with `createSession`, `runMessage`, and `getRun`.
- Produces: `toRunScenario(run: ApiRunView): RunScenario`.
- Consumes: the existing `/v1/sessions`, `/v1/sessions/{id}/messages`, and `/v1/runs/{id}` endpoints.

- [ ] **Step 1: Write failing client and adapter tests**

Test exact URL, method, Bearer header, JSON body, error-code mapping, and conversion of evidence/plan/result into the existing UI model.

- [ ] **Step 2: Run focused tests and verify RED**

Run: `cd web && pnpm test --run src/api/actionguard.test.ts src/domain/adapters.test.ts`

Expected: FAIL because the client and adapter are absent.

- [ ] **Step 3: Implement API mode without removing deterministic mode**

Use `VITE_ACTIONGUARD_API_MODE=demo|live`, `VITE_ACTIONGUARD_API_BASE_URL`, and `VITE_ACTIONGUARD_DEMO_TOKEN`. Add explicit loading, API unavailable, unauthorized, needs-input, waiting-runtime, succeeded, and failed states. Vite proxies `/api` to `http://127.0.0.1:8080` in development.

- [ ] **Step 4: Add API CORS/config support only if the proxy cannot cover the built demo**

Keep CORS origins explicit and fail closed; do not use wildcard origin with credentials.

- [ ] **Step 5: Run Go and web verification**

Run: `go test ./...`

Run: `cd web && pnpm test --run && pnpm build`

Expected: both suites pass.

- [ ] **Step 6: Commit Task 2**

```bash
git add cmd README.md web
git commit -m "feat: connect workbench to Session API"
```

### Task 3: Approval And Event Timeline

**Files:**
- Create: `internal/trace/model.go`
- Create: `internal/trace/store.go`
- Create: `internal/trace/postgres.go`
- Create: `internal/trace/postgres_test.go`
- Create: `web/src/components/ApprovalPanel.tsx`
- Create: `web/src/components/EventTimeline.tsx`
- Create: `web/src/components/ApprovalPanel.test.tsx`
- Modify: `migrations/002_policy_planning.sql`
- Modify: `internal/httpapi/router.go`
- Modify: `internal/httpapi/session_handlers.go`
- Modify: `internal/httpapi/session_handlers_test.go`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Produces: ordered `RunEvent{Sequence, Type, OccurredAt, Payload}` records.
- Produces: `POST /v1/runs/{run_id}/approvals/{approval_id}` and `GET /v1/runs/{run_id}/events`.
- Produces: an approval panel that shows the exact high-risk action, amount, principal, and policy references.

- [ ] **Step 1: Write failing API, store, and component tests**

Cover ownership checks, monotonically increasing sequence, duplicate approval conflict, stale plan-version rejection, and zero tool calls before approval.

- [ ] **Step 2: Verify RED in Go and Vitest**

Run: `go test ./internal/trace ./internal/httpapi`

Run: `cd web && pnpm test --run src/components/ApprovalPanel.test.tsx`

- [ ] **Step 3: Implement event persistence and approval surface**

Approval requests bind `run_id`, `plan_version`, `step_id`, `principal`, and `policy_refs`. A decision for any stale or completed request returns conflict and cannot resume a different plan.

- [ ] **Step 4: Verify all tests and visual states**

Run full Go/web tests and capture waiting, approved, and rejected screenshots.

- [ ] **Step 5: Commit Task 3**

```bash
git add migrations internal web
git commit -m "feat: add approval and run event views"
```

### Task 4: Interview Demo Hardening

**Files:**
- Create: `web/e2e/agent-runs.spec.ts`
- Create: `web/playwright.config.ts`
- Create: `scripts/demo-smoke.sh`
- Modify: `deploy/docker-compose.yml`
- Modify: `Dockerfile`
- Modify: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Produces: `make workbench` for deterministic UI development.
- Produces: `make demo-up` with the web service included.
- Produces: a Playwright flow that exercises the replacement scenario and verifies visual/nonblank states.

- [ ] **Step 1: Write the failing browser flow**

The flow opens Agent Runs, starts replacement, inspects a citation, verifies the malicious note is untrusted, observes approval gating, approves the high-risk step, and verifies final state.

- [ ] **Step 2: Run Playwright and verify RED**

Run: `cd web && pnpm playwright test e2e/agent-runs.spec.ts`

- [ ] **Step 3: Add container and Make targets**

Build the web assets with Node and serve them through a small unprivileged runtime container. Compose must wait for the API health check and expose one documented workbench URL.

- [ ] **Step 4: Run the complete verification matrix**

Run Go tests, web tests/build, Playwright at desktop/mobile sizes, Compose config validation, and the smoke script.

- [ ] **Step 5: Commit Task 4**

```bash
git add Dockerfile Makefile README.md deploy scripts web
git commit -m "test: harden the workbench demo"
```
