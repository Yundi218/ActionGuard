package planningstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

var planningStoreTestTime = time.Date(2026, 8, 13, 9, 30, 0, 0, time.UTC)

func TestPostgresStoreImplementsStore(t *testing.T) {
	var _ Store = (*PostgresStore)(nil)
}

func TestPostgresStoreBindsSessionAccessToOneUser(t *testing.T) {
	store, _, ctx := newPlanningStoreTest(t)
	session := Session{
		ID:        "session-1",
		UserID:    "user-1",
		Region:    "CN",
		CreatedAt: planningStoreTestTime,
	}

	if err := store.CreateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetSession(ctx, session.ID, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if got != session {
		t.Fatalf("session = %#v, want %#v", got, session)
	}

	for _, userID := range []string{"user-2", "missing-user"} {
		if _, err := store.GetSession(ctx, session.ID, userID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetSession for %q error = %v, want ErrNotFound", userID, err)
		}
	}
	if _, err := store.GetSession(ctx, "missing-session", "user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetSession error = %v, want ErrNotFound", err)
	}
}

func TestPostgresStoreCreateRunPersistsUserMessageInSameTransaction(t *testing.T) {
	store, pool, ctx := newPlanningStoreTest(t)
	createPlanningSession(t, store, ctx)
	run := planningRun("run-1")

	if err := store.CreateRun(ctx, run, "Replace my damaged headphones"); err != nil {
		t.Fatal(err)
	}

	var runCount, messageCount int
	if err := pool.QueryRow(ctx, `select count(*) from runs where id = $1`, run.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select count(*)
		from messages
		where run_id = $1 and session_id = $2 and role = 'user'
		  and content = 'Replace my damaged headphones'
	`, run.ID, run.SessionID).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 1 || messageCount != 1 {
		t.Fatalf("persisted counts = runs %d messages %d, want 1 and 1", runCount, messageCount)
	}

	failedRun := planningRun("run-invalid-message")
	err := store.CreateRun(ctx, failedRun, string([]byte{0xff}))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid message error = %v, want ErrInvalid", err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from runs where id = $1`, failedRun.ID).Scan(&runCount); err != nil {
		t.Fatal(err)
	}
	if runCount != 0 {
		t.Fatalf("run count after message failure = %d, want 0", runCount)
	}
}

func TestPostgresStoreSavesPlanningSnapshotsWithCompareAndSet(t *testing.T) {
	store, pool, ctx := newPlanningStoreTest(t)
	createPlanningSession(t, store, ctx)
	if err := store.CreateRun(ctx, planningRun("run-snapshot"), "Plan a replacement"); err != nil {
		t.Fatal(err)
	}

	first := PlanningSnapshot{
		Goal:         "Replace damaged headphones",
		PlanVersion:  1,
		Plan:         json.RawMessage(`{"steps":[{"id":"lookup"}]}`),
		Evidence:     json.RawMessage(`[{"policy_id":"damaged_goods","version":"v3"}]`),
		Verification: json.RawMessage(`{"valid":true}`),
	}
	if err := store.SavePlanningSnapshot(ctx, "run-snapshot", "planning", "ready", first); err != nil {
		t.Fatal(err)
	}

	stale := first
	stale.PlanVersion = 2
	if err := store.SavePlanningSnapshot(ctx, "run-snapshot", "planning", "ready", stale); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale snapshot error = %v, want ErrStateConflict", err)
	}
	var planCount int
	if err := pool.QueryRow(ctx, `select count(*) from plans where run_id = 'run-snapshot'`).Scan(&planCount); err != nil {
		t.Fatal(err)
	}
	if planCount != 1 {
		t.Fatalf("plan count after stale snapshot = %d, want 1", planCount)
	}

	second := PlanningSnapshot{
		Goal:         first.Goal,
		PlanVersion:  2,
		Plan:         json.RawMessage(`{"steps":[{"id":"lookup"},{"id":"replace"}]}`),
		Evidence:     first.Evidence,
		Verification: json.RawMessage(`{"valid":true,"repair":1}`),
	}
	if err := store.SavePlanningSnapshot(ctx, "run-snapshot", "ready", "running", second); err != nil {
		t.Fatal(err)
	}

	view, err := store.GetRunView(ctx, "run-snapshot", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "running" || view.Goal != second.Goal || view.PlanVersion != 2 {
		t.Fatalf("run view state = %#v", view)
	}
	requireJSONEqual(t, view.Plan, second.Plan)
	requireJSONEqual(t, view.Evidence, second.Evidence)
	requireJSONEqual(t, view.Verification, second.Verification)

	if _, err := store.GetRunView(ctx, "run-snapshot", "user-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user GetRunView error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetRunView(ctx, "missing-run", "user-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing GetRunView error = %v, want ErrNotFound", err)
	}
}

func TestPostgresStoreRollsBackSnapshotStateWhenPlanInsertFails(t *testing.T) {
	store, pool, ctx := newPlanningStoreTest(t)
	createPlanningSession(t, store, ctx)
	if err := store.CreateRun(ctx, planningRun("run-snapshot-rollback"), "Plan a replacement"); err != nil {
		t.Fatal(err)
	}

	first := PlanningSnapshot{
		Goal:         "Original goal",
		PlanVersion:  1,
		Plan:         json.RawMessage(`{"steps":[{"id":"lookup"}]}`),
		Evidence:     json.RawMessage(`[]`),
		Verification: json.RawMessage(`{"valid":true}`),
	}
	if err := store.SavePlanningSnapshot(ctx, "run-snapshot-rollback", "planning", "ready", first); err != nil {
		t.Fatal(err)
	}

	duplicate := first
	duplicate.Goal = "Goal that must roll back"
	if err := store.SavePlanningSnapshot(ctx, "run-snapshot-rollback", "ready", "running", duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate snapshot error = %v, want ErrConflict", err)
	}

	var status, goal string
	if err := pool.QueryRow(ctx, `
		select status, goal
		from runs
		where id = 'run-snapshot-rollback'
	`).Scan(&status, &goal); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || goal != first.Goal {
		t.Fatalf("run state after failed plan insert = status %q goal %q, want ready and %q", status, goal, first.Goal)
	}

	var planCount int
	if err := pool.QueryRow(ctx, `select count(*) from plans where run_id = 'run-snapshot-rollback'`).Scan(&planCount); err != nil {
		t.Fatal(err)
	}
	if planCount != 1 {
		t.Fatalf("plan count after failed plan insert = %d, want 1", planCount)
	}
}

func TestPostgresStoreTransitionsRunWithTypedPublicResult(t *testing.T) {
	store, pool, ctx := newPlanningStoreTest(t)
	createPlanningSession(t, store, ctx)
	if err := store.CreateRun(ctx, planningRun("run-transition"), "Check my order"); err != nil {
		t.Fatal(err)
	}

	result := RunResult{
		Summary: "replacement created",
		Steps: []RunStepResult{{
			StepID:   "replace",
			Tool:     "create_replacement",
			Status:   "succeeded",
			Replayed: true,
		}},
	}
	if err := store.TransitionRun(ctx, "run-transition", "planning", "succeeded", result, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.TransitionRun(ctx, "run-transition", "planning", "failed", RunResult{Summary: "stale"}, "stale", "stale transition"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("stale transition error = %v, want ErrStateConflict", err)
	}

	view, err := store.GetRunView(ctx, "run-transition", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "succeeded" || view.FailureCode != "" {
		t.Fatalf("run view = %#v", view)
	}
	if !reflect.DeepEqual(view.Result, result) {
		t.Fatalf("result = %#v, want %#v", view.Result, result)
	}

	var persisted json.RawMessage
	if err := pool.QueryRow(ctx, `select result_json from runs where id = 'run-transition'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	requireJSONEqual(t, persisted, wantJSON)
}

func TestPostgresStoreRunViewDropsUnknownResultFieldsAndInternalFailureDetail(t *testing.T) {
	store, pool, ctx := newPlanningStoreTest(t)
	createPlanningSession(t, store, ctx)
	if err := store.CreateRun(ctx, planningRun("run-public-view"), "Check my order"); err != nil {
		t.Fatal(err)
	}

	const internalDetail = "provider response contained api-key=secret and database_private.sql failed"
	if _, err := pool.Exec(ctx, `
		update runs
		set status = 'failed',
		    failure_code = 'provider_unavailable',
		    failure_detail = $2,
		    result_json = $3::jsonb
		where id = $1
	`, "run-public-view", internalDetail, `{
		"summary":"Planning could not complete",
		"provider_response":"secret provider body",
		"database_error":"select * from database_private",
		"steps":[{
			"step_id":"lookup",
			"tool":"get_order",
			"status":"failed",
			"trusted":{"order_id":"private-order"},
			"untrusted_text":{"note":"provider-controlled"}
		}]
	}`); err != nil {
		t.Fatal(err)
	}

	view, err := store.GetRunView(ctx, "run-public-view", "user-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.FailureCode != "provider_unavailable" {
		t.Fatalf("failure code = %q, want provider_unavailable", view.FailureCode)
	}
	if view.Result.Summary != "Planning could not complete" || len(view.Result.Steps) != 1 {
		t.Fatalf("public result = %#v", view.Result)
	}

	publicJSON, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		internalDetail,
		"failure_detail",
		"provider_response",
		"database_error",
		"trusted",
		"untrusted_text",
		"private-order",
		"provider-controlled",
	} {
		if strings.Contains(string(publicJSON), forbidden) {
			t.Fatalf("public run view contains forbidden %q: %s", forbidden, publicJSON)
		}
	}
}

func TestPostgresStoreMapsDatabaseErrorsWithoutSQLDetails(t *testing.T) {
	store, pool, ctx := newPlanningStoreTest(t)

	invalid := planningRun("invalid-status")
	invalid.SessionID = "missing-session"
	if err := store.CreateRun(ctx, invalid, "message"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session error = %v, want ErrNotFound", err)
	}

	pool.Close()
	err := store.CreateSession(ctx, Session{ID: "closed", UserID: "user-1", Region: "CN", CreatedAt: planningStoreTestTime})
	if !errors.Is(err, ErrStore) {
		t.Fatalf("closed pool error = %v, want ErrStore", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "insert") || strings.Contains(strings.ToLower(err.Error()), "sql") {
		t.Fatalf("store error leaks SQL details: %q", err)
	}
}

func newPlanningStoreTest(t *testing.T) (*PostgresStore, *pgxpool.Pool, context.Context) {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	adminPool, err := database.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	schema := fmt.Sprintf("planning_store_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	migrationURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := migrationURL.Query()
	query.Set("search_path", schema+",public")
	migrationURL.RawQuery = query.Encode()

	pool, err := database.Open(ctx, migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into users (id, display_name)
		values ('user-1', 'First User'), ('user-2', 'Second User')
	`); err != nil {
		t.Fatal(err)
	}
	return NewPostgresStore(pool), pool, ctx
}

func createPlanningSession(t *testing.T, store *PostgresStore, ctx context.Context) {
	t.Helper()
	if err := store.CreateSession(ctx, Session{
		ID:        "session-1",
		UserID:    "user-1",
		Region:    "CN",
		CreatedAt: planningStoreTestTime,
	}); err != nil {
		t.Fatal(err)
	}
}

func planningRun(id string) Run {
	return Run{
		ID:        id,
		SessionID: "session-1",
		Status:    "planning",
		Goal:      "",
		CreatedAt: planningStoreTestTime,
		UpdatedAt: planningStoreTestTime,
	}
}

func requireJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}
