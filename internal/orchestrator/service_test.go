package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/auth"
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/llm"
	"github.com/Yundi218/ActionGuard/internal/planningstore"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/querycontext"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/Yundi218/ActionGuard/internal/tools"
	"github.com/Yundi218/ActionGuard/internal/verifier"
)

type recordingStore struct {
	session       planningstore.Session
	runs          map[string]planningstore.RunView
	calls         *[]string
	getSessionErr error
}

func (s *recordingStore) CreateSession(_ context.Context, session planningstore.Session) error {
	*s.calls = append(*s.calls, "create_session")
	s.session = session
	return nil
}
func (s *recordingStore) GetSession(_ context.Context, id, user string) (planningstore.Session, error) {
	*s.calls = append(*s.calls, "get_session")
	if s.getSessionErr != nil {
		return planningstore.Session{}, s.getSessionErr
	}
	if id != s.session.ID || user != s.session.UserID {
		return planningstore.Session{}, planningstore.ErrNotFound
	}
	return s.session, nil
}
func (s *recordingStore) CreateRun(_ context.Context, run planningstore.Run, _ string) error {
	*s.calls = append(*s.calls, "create_run")
	if s.runs == nil {
		s.runs = map[string]planningstore.RunView{}
	}
	s.runs[run.ID] = planningstore.RunView{ID: run.ID, SessionID: run.SessionID, Status: run.Status, Goal: run.Goal}
	return nil
}
func (s *recordingStore) SavePlanningSnapshot(_ context.Context, id, from, to string, snapshot planningstore.PlanningSnapshot) error {
	*s.calls = append(*s.calls, "snapshot:"+from+":"+to)
	v := s.runs[id]
	v.Status = to
	v.Goal = snapshot.Goal
	v.PlanVersion = snapshot.PlanVersion
	v.Plan = append([]byte(nil), snapshot.Plan...)
	v.Evidence = append([]byte(nil), snapshot.Evidence...)
	v.Verification = append([]byte(nil), snapshot.Verification...)
	s.runs[id] = v
	return nil
}
func (s *recordingStore) TransitionRun(_ context.Context, id, from, to string, result planningstore.RunResult, code, _ string) error {
	*s.calls = append(*s.calls, "transition:"+from+":"+to)
	v := s.runs[id]
	v.Status = to
	v.Result = result
	v.FailureCode = code
	s.runs[id] = v
	return nil
}
func (s *recordingStore) GetRunView(_ context.Context, id, user string) (planningstore.RunView, error) {
	*s.calls = append(*s.calls, "get_run")
	if user != s.session.UserID {
		return planningstore.RunView{}, planningstore.ErrNotFound
	}
	v, ok := s.runs[id]
	if !ok {
		return planningstore.RunView{}, planningstore.ErrNotFound
	}
	return v, nil
}

type fakeResolver struct {
	calls  *[]string
	result querycontext.Context
	err    error
}

func (f fakeResolver) Resolve(_ context.Context, user, region, message string) (querycontext.Context, error) {
	*f.calls = append(*f.calls, "resolve:"+user+":"+region+":"+message)
	return f.result, f.err
}

type fakeRetriever struct {
	calls    *[]string
	got      policy.Query
	evidence []policy.Evidence
	err      error
}

func (f *fakeRetriever) Search(_ context.Context, q policy.Query) ([]policy.Evidence, error) {
	*f.calls = append(*f.calls, "retrieve")
	f.got = q
	return cloneEvidenceForTest(f.evidence), f.err
}

type fakePlanner struct {
	calls     *[]string
	plans     []agent.ActionPlan
	planErr   error
	repairErr error
}

func (f *fakePlanner) Plan(_ context.Context, r llm.PlanRequest) (agent.ActionPlan, error) {
	*f.calls = append(*f.calls, "plan")
	if len(r.ToolContracts) != len(toolkit.Registry()) {
		return agent.ActionPlan{}, errors.New("contracts")
	}
	if f.planErr != nil {
		return agent.ActionPlan{}, f.planErr
	}
	return f.plans[0], nil
}
func (f *fakePlanner) Repair(_ context.Context, _ llm.RepairRequest) (agent.ActionPlan, error) {
	*f.calls = append(*f.calls, "repair")
	if f.repairErr != nil {
		return agent.ActionPlan{}, f.repairErr
	}
	return f.plans[1], nil
}

type fakeExecutor struct {
	calls  *[]string
	result tools.ExecutionResult
	err    error
}

func (f fakeExecutor) Execute(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
	*f.calls = append(*f.calls, "execute:"+r.Plan.UserID())
	return f.result, f.err
}

type fakeFacts struct{}

func (fakeFacts) GetOrder(_ context.Context, user, id string) (commerce.Order, error) {
	return commerce.Order{ID: id, UserID: user, SKU: "HP-71", Status: "delivered", PaidAmountCents: 10000}, nil
}

func TestRunMessageChecksOwnershipBeforeGeneratingOrCallingDependencies(t *testing.T) {
	calls := []string{}
	store := &recordingStore{session: planningstore.Session{ID: "owned", UserID: "user_018", Region: "CN"}, calls: &calls, getSessionErr: planningstore.ErrNotFound}
	service := newTestService(t, store, &calls, nil, nil)
	_, err := service.RunMessage(context.Background(), auth.Principal{UserID: "user_999", Scopes: []string{"order:read"}}, "owned", "hello")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(calls, []string{"get_session"}) {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestNewRejectsTypedNilDependencies(t *testing.T) {
	var store *recordingStore
	_, err := New(Dependencies{Store: store, Resolver: fakeResolver{}, Retriever: &fakeRetriever{}, Planner: &fakePlanner{}, Verifier: verifier.New(fakeFacts{}), Executor: fakeExecutor{}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestCreateSessionGeneratesCanonicalUUIDv4AndRejectsEntropyFailure(t *testing.T) {
	calls := []string{}
	store := &recordingStore{calls: &calls}
	service := newTestService(t, store, &calls, nil, nil)
	view, err := service.CreateSession(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}, "CN")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(view.SessionID) {
		t.Fatalf("session id=%q", view.SessionID)
	}
	service.random = strings.NewReader("short")
	if _, err := service.CreateSession(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}, "CN"); !errors.Is(err, ErrService) {
		t.Fatalf("entropy error=%v", err)
	}
}

func TestRunMessageExecutesValidPlanInRequiredOrderAndProjectsEvidence(t *testing.T) {
	calls := []string{}
	store := &recordingStore{session: planningstore.Session{ID: "session", UserID: "user_018", Region: "CN"}, calls: &calls}
	evidence := []policy.Evidence{{ChunkID: "chunk", CitationID: "damaged_goods:v3:rule:0-10", PolicyID: "damaged_goods", Version: "v3", Section: "rule", StartOffset: 0, EndOffset: 10, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EffectiveTo: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Region: "CN", ProductCategory: "electronics", RiskLevel: toolkit.Write, Text: "secret policy prose"}}
	plan := agent.ActionPlan{Goal: "lookup", PolicyRefs: []string{}, Steps: []agent.Step{{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "order.exists"}}}
	retriever := &fakeRetriever{calls: &calls, evidence: evidence}
	planner := &fakePlanner{calls: &calls, plans: []agent.ActionPlan{plan}}
	service := newTestService(t, store, &calls, retriever, planner)
	view, err := service.RunMessage(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}, "session", "Order AG-1042")
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "succeeded" || view.Plan == nil || len(view.Evidence) != 1 || view.Evidence[0].PolicyID != "damaged_goods" {
		t.Fatalf("view=%#v", view)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "secret policy prose") {
		t.Fatalf("public view leaked evidence: %s", encoded)
	}
	want := []string{"get_session", "create_run", "resolve:user_018:CN:Order AG-1042", "retrieve", "plan", "snapshot:planning:ready", "transition:ready:running", "execute:user_018", "transition:running:succeeded", "get_run"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=\n%#v\nwant=\n%#v", calls, want)
	}
	if retriever.got.Region != "CN" || retriever.got.ProductCategory != "electronics" || retriever.got.Limit != 5 || retriever.got.Text != "Order AG-1042" {
		t.Fatalf("query=%#v", retriever.got)
	}
}

func TestRunMessageNeedsInputStopsBeforeRetrieval(t *testing.T) {
	calls := []string{}
	store := &recordingStore{session: planningstore.Session{ID: "session", UserID: "user_018", Region: "CN"}, calls: &calls}
	service := newTestService(t, store, &calls, nil, nil)
	service.resolver = fakeResolver{calls: &calls, err: querycontext.ErrNeedsInput}
	view, err := service.RunMessage(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}, "session", "replace it")
	if err != nil || view.Status != "needs_input" {
		t.Fatalf("view=%#v err=%v", view, err)
	}
	want := []string{"get_session", "create_run", "resolve:user_018:CN:replace it", "transition:planning:needs_input", "get_run"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%#v", calls)
	}
}

func TestRunMessageRepairsExactlyOnceThenFails(t *testing.T) {
	calls := []string{}
	store := &recordingStore{session: planningstore.Session{ID: "session", UserID: "user_018", Region: "CN"}, calls: &calls}
	bad := agent.ActionPlan{Goal: "bad", PolicyRefs: []string{}, Steps: []agent.Step{{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Write, SuccessCondition: "order.exists"}}}
	retriever := &fakeRetriever{calls: &calls}
	planner := &fakePlanner{calls: &calls, plans: []agent.ActionPlan{bad, bad}}
	service := newTestService(t, store, &calls, retriever, planner)
	view, err := service.RunMessage(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}, "session", "Order AG-1042")
	if err != nil || view.Status != "failed" || view.PlanVersion != 2 {
		t.Fatalf("view=%#v err=%v", view, err)
	}
	joined := strings.Join(calls, ",")
	if strings.Count(joined, "repair") != 1 || strings.Contains(joined, "execute") {
		t.Fatalf("calls=%s", joined)
	}
}

func TestRunMessagePersistsSanitizedExecutorFailure(t *testing.T) {
	calls := []string{}
	store := &recordingStore{session: planningstore.Session{ID: "session", UserID: "user_018", Region: "CN"}, calls: &calls}
	plan := agent.ActionPlan{Goal: "lookup", PolicyRefs: []string{}, Steps: []agent.Step{{ID: "order", Tool: "get_order", Arguments: json.RawMessage(`{"order_id":"AG-1042"}`), DependsOn: []string{}, Risk: toolkit.Read, SuccessCondition: "order.exists"}}}
	service := newTestService(t, store, &calls, &fakeRetriever{calls: &calls}, &fakePlanner{calls: &calls, plans: []agent.ActionPlan{plan}})
	service.executor = fakeExecutor{calls: &calls, err: errors.New("secret upstream body")}
	view, err := service.RunMessage(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read"}}, "session", "Order AG-1042")
	if err != nil || view.Status != "failed" {
		t.Fatalf("view=%#v err=%v", view, err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "secret") || strings.Contains(store.runs[view.RunID].FailureCode, "secret") {
		t.Fatalf("failure leaked: %s", encoded)
	}
}

func TestRunMessageTransitionsHighRiskPlanToWaitingRuntime(t *testing.T) {
	calls := []string{}
	store := &recordingStore{session: planningstore.Session{ID: "session", UserID: "user_018", Region: "CN"}, calls: &calls}
	capCents := int64(2000)
	at := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	evidence := []policy.Evidence{
		{CitationID: "damaged_goods:v3:rule:0-10", PolicyID: "damaged_goods", Version: "v3", Section: "rule", EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EffectiveTo: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Region: "CN", ProductCategory: "electronics", RiskLevel: toolkit.Write},
		{CitationID: "customer_care:v1:rule:0-10", PolicyID: "customer_care", Version: "v1", Section: "rule", EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), EffectiveTo: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Region: "CN", ProductCategory: "electronics", RiskLevel: toolkit.HighRiskWrite, MaxCouponCents: &capCents},
	}
	retriever := &fakeRetriever{calls: &calls, evidence: evidence}
	service := newTestService(t, store, &calls, retriever, &fakePlanner{calls: &calls, plans: []agent.ActionPlan{{}}})
	service.planner = llm.NewFixturePlanner()
	service.executor = fakeExecutor{calls: &calls, result: tools.ExecutionResult{Status: tools.StatusWaitingRuntime}}
	service.resolver = fakeResolver{calls: &calls, result: querycontext.Context{OrderID: "AG-1042", Region: "CN", ProductCategory: "electronics", At: at}}
	view, err := service.RunMessage(context.Background(), auth.Principal{UserID: "user_018", Scopes: []string{"order:read", "eligibility:read", "inventory:read", "replacement:write", "coupon:write"}}, "session", llm.FixtureReplacementCouponMessage)
	if err != nil || view.Status != "waiting_runtime" || len(view.Result.Steps) != 0 {
		t.Fatalf("view=%#v err=%v", view, err)
	}
}

func newTestService(t *testing.T, store *recordingStore, calls *[]string, retriever *fakeRetriever, planner *fakePlanner) *Service {
	t.Helper()
	if retriever == nil {
		retriever = &fakeRetriever{calls: calls}
	}
	if planner == nil {
		planner = &fakePlanner{calls: calls, plans: []agent.ActionPlan{{}}}
	}
	service, err := New(Dependencies{Store: store, Resolver: fakeResolver{calls: calls, result: querycontext.Context{OrderID: "AG-1042", Region: "CN", ProductCategory: "electronics", At: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}}, Retriever: retriever, Planner: planner, Verifier: verifier.New(fakeFacts{}), Executor: fakeExecutor{calls: calls, result: tools.ExecutionResult{Status: tools.StatusSucceeded, Steps: []tools.StepResult{{StepID: "order", Tool: "get_order", Trusted: json.RawMessage(`{"secret":"value"}`), UntrustedText: map[string]string{"note": "untrusted"}}}}}, Now: func() time.Time { return time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) }, Random: strings.NewReader(strings.Repeat("a", 64))})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func cloneEvidenceForTest(values []policy.Evidence) []policy.Evidence {
	out := append([]policy.Evidence(nil), values...)
	for i := range out {
		if out[i].MaxCouponCents != nil {
			v := *out[i].MaxCouponCents
			out[i].MaxCouponCents = &v
		}
	}
	return out
}
