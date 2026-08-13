package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Yundi218/ActionGuard/internal/agent"
	"github.com/Yundi218/ActionGuard/internal/auth"
	"github.com/Yundi218/ActionGuard/internal/llm"
	"github.com/Yundi218/ActionGuard/internal/planningstore"
	"github.com/Yundi218/ActionGuard/internal/policy"
	"github.com/Yundi218/ActionGuard/internal/querycontext"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/Yundi218/ActionGuard/internal/tools"
	"github.com/Yundi218/ActionGuard/internal/verifier"
)

var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("resource not found")
	ErrConflict = errors.New("state conflict")
	ErrCanceled = errors.New("request canceled")
	ErrService  = errors.New("service unavailable")
)

const (
	cancellationCleanupTimeout = 500 * time.Millisecond
	defaultExecutionTimeout    = 35 * time.Second
	terminalPersistenceTimeout = 2 * time.Second
)

type Store interface {
	planningstore.Store
}

type ContextResolver interface {
	Resolve(context.Context, string, string, string) (querycontext.Context, error)
}

type PolicyRetriever interface {
	Search(context.Context, policy.Query) ([]policy.Evidence, error)
}

type PlanVerifier interface {
	VerifyAndSeal(context.Context, agent.ActionPlan, verifier.Context) (verifier.VerifiedPlan, verifier.Result)
}

type PlanExecutor interface {
	Execute(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error)
}

type Dependencies struct {
	Store            Store
	Resolver         ContextResolver
	Retriever        PolicyRetriever
	Planner          llm.Planner
	Verifier         PlanVerifier
	Executor         PlanExecutor
	ExecutionTimeout time.Duration
	Now              func() time.Time
	Random           io.Reader
}

type Service struct {
	store            Store
	resolver         ContextResolver
	retriever        PolicyRetriever
	planner          llm.Planner
	verifier         PlanVerifier
	executor         PlanExecutor
	executionTimeout time.Duration
	now              func() time.Time
	random           io.Reader
}

type EvidenceView struct {
	ChunkID         string       `json:"chunk_id"`
	CitationID      string       `json:"citation_id"`
	PolicyID        string       `json:"policy_id"`
	Version         string       `json:"version"`
	Section         string       `json:"section"`
	StartOffset     int          `json:"start_offset"`
	EndOffset       int          `json:"end_offset"`
	EffectiveFrom   time.Time    `json:"effective_from"`
	EffectiveTo     time.Time    `json:"effective_to"`
	Region          string       `json:"region"`
	ProductCategory string       `json:"product_category"`
	RiskLevel       toolkit.Risk `json:"risk_level"`
	MaxCouponCents  *int64       `json:"max_coupon_cents,omitempty"`
}

type SessionView struct {
	SessionID string    `json:"session_id"`
	Region    string    `json:"region"`
	CreatedAt time.Time `json:"created_at"`
}

type RunView struct {
	RunID        string                  `json:"run_id"`
	SessionID    string                  `json:"session_id"`
	Status       string                  `json:"status"`
	Goal         string                  `json:"goal"`
	Plan         *agent.ActionPlan       `json:"plan"`
	Verification *verifier.Result        `json:"verification"`
	Evidence     []EvidenceView          `json:"evidence"`
	Result       planningstore.RunResult `json:"result"`
	PlanVersion  int                     `json:"-"`
}

func New(dependencies Dependencies) (*Service, error) {
	if nilDependency(dependencies.Store) || nilDependency(dependencies.Resolver) || nilDependency(dependencies.Retriever) || nilDependency(dependencies.Planner) || nilDependency(dependencies.Verifier) || nilDependency(dependencies.Executor) {
		return nil, ErrInvalid
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.ExecutionTimeout < 0 {
		return nil, ErrInvalid
	}
	if dependencies.ExecutionTimeout == 0 {
		dependencies.ExecutionTimeout = defaultExecutionTimeout
	}
	if dependencies.Random == nil {
		dependencies.Random = rand.Reader
	}
	return &Service{store: dependencies.Store, resolver: dependencies.Resolver, retriever: dependencies.Retriever, planner: dependencies.Planner, verifier: dependencies.Verifier, executor: dependencies.Executor, executionTimeout: dependencies.ExecutionTimeout, now: dependencies.Now, random: dependencies.Random}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (service *Service) CreateSession(ctx context.Context, principal auth.Principal, region string) (SessionView, error) {
	principal, err := validatePrincipal(principal)
	if err != nil || region != "CN" {
		return SessionView{}, ErrInvalid
	}
	id, err := service.newID()
	if err != nil {
		return SessionView{}, ErrService
	}
	createdAt := service.now().UTC()
	if err := service.store.CreateSession(ctx, planningstore.Session{ID: id, UserID: principal.UserID, Region: region, CreatedAt: createdAt}); err != nil {
		return SessionView{}, mapStoreError(ctx, err)
	}
	return SessionView{SessionID: id, Region: region, CreatedAt: createdAt}, nil
}

func (service *Service) RunMessage(ctx context.Context, principal auth.Principal, sessionID, message string) (RunView, error) {
	principal, err := validatePrincipal(principal)
	if err != nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(message) == "" || !utf8.ValidString(sessionID) || !utf8.ValidString(message) {
		return RunView{}, ErrInvalid
	}
	session, err := service.store.GetSession(ctx, sessionID, principal.UserID)
	if err != nil {
		return RunView{}, mapStoreError(ctx, err)
	}
	if session.Region != "CN" {
		return RunView{}, ErrInvalid
	}
	runID, err := service.newID()
	if err != nil {
		return RunView{}, ErrService
	}
	now := service.now().UTC()
	if err := service.store.CreateRun(ctx, planningstore.Run{ID: runID, SessionID: session.ID, Status: "planning", Goal: message, CreatedAt: now, UpdatedAt: now}, message); err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		return RunView{}, mapStoreError(ctx, err)
	}
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}

	resolved, err := service.resolver.Resolve(ctx, principal.UserID, session.Region, message)
	if err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		if errors.Is(err, querycontext.ErrNeedsInput) {
			if transitionErr := service.store.TransitionRun(ctx, runID, "planning", "needs_input", planningstore.RunResult{Summary: "additional input required"}, "", ""); transitionErr != nil {
				if isCancellation(ctx, transitionErr) {
					return service.cancelCreatedRun(runID)
				}
				return RunView{}, mapStoreError(ctx, transitionErr)
			}
			return service.getCreatedRun(ctx, principal, runID)
		}
		return service.failAndGet(ctx, principal, runID, "planning", "context_resolution_failed")
	}
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}
	evidence, err := service.retriever.Search(ctx, policy.Query{Text: message, At: resolved.At, Region: resolved.Region, ProductCategory: resolved.ProductCategory, Limit: 5})
	if err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		return service.failAndGet(ctx, principal, runID, "planning", "retrieval_failed")
	}
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}
	if !validEvidence(evidence) {
		return service.failAndGet(ctx, principal, runID, "planning", "retrieval_failed")
	}
	evidence = cloneEvidence(evidence)
	request := llm.PlanRequest{UserMessage: message, Evidence: cloneEvidence(evidence), ToolContracts: toolkit.Registry()}
	plan, err := service.planner.Plan(ctx, clonePlanRequest(request))
	if err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		return service.failAndGet(ctx, principal, runID, "planning", "planning_failed")
	}
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}
	plan, err = canonicalPlan(plan)
	if err != nil {
		return service.failAndGet(ctx, principal, runID, "planning", "planning_failed")
	}
	sealed, verification := service.verifier.VerifyAndSeal(ctx, clonePlan(plan), verifier.Context{UserID: principal.UserID, ResolvedOrderID: resolved.OrderID, Scopes: append([]string(nil), principal.Scopes...), Now: resolved.At, Evidence: cloneEvidence(evidence)})
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}
	if !verification.Valid {
		if err := service.saveSnapshot(ctx, runID, "planning", "planning", 1, plan, evidence, verification); err != nil {
			if isCancellation(ctx, err) {
				return service.cancelCreatedRun(runID)
			}
			return RunView{}, err
		}
		if ctx.Err() != nil {
			return service.cancelCreatedRun(runID)
		}
		repaired, repairErr := service.planner.Repair(ctx, llm.RepairRequest{Original: clonePlanRequest(request), Plan: clonePlan(plan), Errors: append([]verifier.Error(nil), verification.Errors...)})
		if repairErr != nil {
			if isCancellation(ctx, repairErr) {
				return service.cancelCreatedRun(runID)
			}
			return service.failAndGet(ctx, principal, runID, "planning", "planning_failed")
		}
		if ctx.Err() != nil {
			return service.cancelCreatedRun(runID)
		}
		plan, repairErr = canonicalPlan(repaired)
		if repairErr != nil {
			return service.failAndGet(ctx, principal, runID, "planning", "planning_failed")
		}
		sealed, verification = service.verifier.VerifyAndSeal(ctx, clonePlan(plan), verifier.Context{UserID: principal.UserID, ResolvedOrderID: resolved.OrderID, Scopes: append([]string(nil), principal.Scopes...), Now: resolved.At, Evidence: cloneEvidence(evidence)})
		if ctx.Err() != nil {
			return service.cancelCreatedRun(runID)
		}
		if !verification.Valid {
			if err := service.saveSnapshot(ctx, runID, "planning", "planning", 2, plan, evidence, verification); err != nil {
				if isCancellation(ctx, err) {
					return service.cancelCreatedRun(runID)
				}
				return RunView{}, err
			}
			if ctx.Err() != nil {
				return service.cancelCreatedRun(runID)
			}
			return service.failAndGet(ctx, principal, runID, "planning", "verification_failed")
		}
		if err := service.saveSnapshot(ctx, runID, "planning", "ready", 2, plan, evidence, verification); err != nil {
			if isCancellation(ctx, err) {
				return service.cancelCreatedRun(runID)
			}
			return RunView{}, err
		}
	} else if err := service.saveSnapshot(ctx, runID, "planning", "ready", 1, plan, evidence, verification); err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		return RunView{}, err
	}
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}

	if err := service.store.TransitionRun(ctx, runID, "ready", "running", planningstore.RunResult{}, "", ""); err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		return RunView{}, mapStoreError(ctx, err)
	}
	return service.executeAndPersist(runID, principal, sealed)
}

func (service *Service) executeAndPersist(runID string, principal auth.Principal, sealed verifier.VerifiedPlan) (RunView, error) {
	executionContext, cancelExecution := context.WithTimeout(context.Background(), service.executionTimeout)
	execution, executeErr := service.executor.Execute(executionContext, tools.ExecutionRequest{RunID: runID, Plan: sealed})
	cancelExecution()
	target := execution.Status
	if executeErr != nil || target != tools.StatusSucceeded && target != tools.StatusWaitingRuntime && target != tools.StatusFailed {
		target = tools.StatusFailed
	}
	result := sanitizeExecution(execution)
	code := ""
	detail := ""
	if target == tools.StatusFailed {
		code = "execution_failed"
		detail = "execution_failed"
	}
	persistenceContext, cancelPersistence := context.WithTimeout(context.Background(), terminalPersistenceTimeout)
	defer cancelPersistence()
	if err := service.store.TransitionRun(persistenceContext, runID, "running", target, result, code, detail); err != nil {
		return RunView{}, mapStoreError(persistenceContext, err)
	}
	return service.GetRun(persistenceContext, principal, runID)
}

func (service *Service) GetRun(ctx context.Context, principal auth.Principal, runID string) (RunView, error) {
	principal, err := validatePrincipal(principal)
	if err != nil || strings.TrimSpace(runID) == "" {
		return RunView{}, ErrInvalid
	}
	stored, err := service.store.GetRunView(ctx, runID, principal.UserID)
	if err != nil {
		return RunView{}, mapStoreError(ctx, err)
	}
	return projectRun(stored)
}

func (service *Service) saveSnapshot(ctx context.Context, runID, from, to string, version int, plan agent.ActionPlan, evidence []policy.Evidence, result verifier.Result) error {
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return ErrService
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return ErrService
	}
	verificationJSON, err := json.Marshal(result)
	if err != nil {
		return ErrService
	}
	if err := service.store.SavePlanningSnapshot(ctx, runID, from, to, planningstore.PlanningSnapshot{Goal: plan.Goal, PlanVersion: version, Plan: planJSON, Evidence: evidenceJSON, Verification: verificationJSON}); err != nil {
		return mapStoreError(ctx, err)
	}
	return nil
}

func (service *Service) failAndGet(ctx context.Context, principal auth.Principal, runID, from, code string) (RunView, error) {
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}
	if err := service.store.TransitionRun(ctx, runID, from, "failed", planningstore.RunResult{}, code, code); err != nil {
		if isCancellation(ctx, err) {
			return service.cancelCreatedRun(runID)
		}
		return RunView{}, mapStoreError(ctx, err)
	}
	if ctx.Err() != nil {
		return service.cancelCreatedRun(runID)
	}
	return service.getCreatedRun(ctx, principal, runID)
}

func (service *Service) getCreatedRun(ctx context.Context, principal auth.Principal, runID string) (RunView, error) {
	view, err := service.GetRun(ctx, principal, runID)
	if err != nil && isCancellation(ctx, err) {
		return service.cancelCreatedRun(runID)
	}
	return view, err
}

func (service *Service) cancelCreatedRun(runID string) (RunView, error) {
	service.convergeCanceledRun(runID)
	return RunView{}, ErrCanceled
}

func (service *Service) convergeCanceledRun(runID string) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), cancellationCleanupTimeout)
	defer cancel()
	for _, from := range []string{"planning", "ready", "running"} {
		err := service.store.TransitionRun(cleanupContext, runID, from, "failed", planningstore.RunResult{}, "request_canceled", "request_canceled")
		if err == nil || errors.Is(err, planningstore.ErrNotFound) {
			return
		}
		if cleanupContext.Err() != nil {
			return
		}
		if !errors.Is(err, planningstore.ErrStateConflict) {
			return
		}
	}
}

func isCancellation(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrCanceled)
}

func (service *Service) newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := io.ReadFull(service.random, bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

func validatePrincipal(principal auth.Principal) (auth.Principal, error) {
	if !validPrincipalValue(principal.UserID, false) || len(principal.Scopes) == 0 {
		return auth.Principal{}, ErrInvalid
	}
	scopes := append([]string(nil), principal.Scopes...)
	for _, scope := range scopes {
		if !validPrincipalValue(scope, true) {
			return auth.Principal{}, ErrInvalid
		}
	}
	sort.Strings(scopes)
	scopes = deduplicate(scopes)
	principal.Scopes = scopes
	return principal, nil
}

func validPrincipalValue(value string, allowColon bool) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' || allowColon && character == ':' {
			continue
		}
		return false
	}
	return true
}

func deduplicate(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func mapStoreError(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrCanceled
	}
	switch {
	case errors.Is(err, planningstore.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, planningstore.ErrStateConflict), errors.Is(err, planningstore.ErrConflict):
		return ErrConflict
	case errors.Is(err, planningstore.ErrInvalid):
		return ErrInvalid
	default:
		return ErrService
	}
}

func projectRun(stored planningstore.RunView) (RunView, error) {
	view := RunView{RunID: stored.ID, SessionID: stored.SessionID, Status: stored.Status, Goal: stored.Goal, Evidence: []EvidenceView{}, Result: stored.Result}
	view.PlanVersion = stored.PlanVersion
	if len(stored.Plan) > 0 && string(stored.Plan) != "null" {
		plan, err := agent.DecodeActionPlan(stored.Plan)
		if err != nil {
			return RunView{}, ErrService
		}
		view.Plan = &plan
	}
	if len(stored.Verification) > 0 && string(stored.Verification) != "null" {
		var result verifier.Result
		if err := json.Unmarshal(stored.Verification, &result); err != nil {
			return RunView{}, ErrService
		}
		view.Verification = &result
	}
	if len(stored.Evidence) > 0 && string(stored.Evidence) != "null" {
		var evidence []policy.Evidence
		if err := json.Unmarshal(stored.Evidence, &evidence); err != nil {
			return RunView{}, ErrService
		}
		view.Evidence = projectEvidence(evidence)
	}
	return view, nil
}

func projectEvidence(evidence []policy.Evidence) []EvidenceView {
	out := make([]EvidenceView, len(evidence))
	for index, item := range evidence {
		var capCopy *int64
		if item.MaxCouponCents != nil {
			v := *item.MaxCouponCents
			capCopy = &v
		}
		out[index] = EvidenceView{ChunkID: item.ChunkID, CitationID: item.CitationID, PolicyID: item.PolicyID, Version: item.Version, Section: item.Section, StartOffset: item.StartOffset, EndOffset: item.EndOffset, EffectiveFrom: item.EffectiveFrom, EffectiveTo: item.EffectiveTo, Region: item.Region, ProductCategory: item.ProductCategory, RiskLevel: item.RiskLevel, MaxCouponCents: capCopy}
	}
	return out
}
func cloneEvidence(in []policy.Evidence) []policy.Evidence {
	out := append([]policy.Evidence(nil), in...)
	for i := range out {
		if out[i].MaxCouponCents != nil {
			v := *out[i].MaxCouponCents
			out[i].MaxCouponCents = &v
		}
	}
	return out
}
func clonePlan(plan agent.ActionPlan) agent.ActionPlan {
	cloned, _ := canonicalPlan(plan)
	return cloned
}
func clonePlanRequest(request llm.PlanRequest) llm.PlanRequest {
	return llm.PlanRequest{UserMessage: request.UserMessage, Evidence: cloneEvidence(request.Evidence), ToolContracts: toolkit.Registry()}
}
func sanitizeExecution(execution tools.ExecutionResult) planningstore.RunResult {
	result := planningstore.RunResult{Steps: make([]planningstore.RunStepResult, len(execution.Steps))}
	if execution.Status == tools.StatusSucceeded {
		result.Summary = "execution completed"
	} else if execution.Status == tools.StatusWaitingRuntime {
		result.Summary = "runtime approval required"
	} else {
		result.Summary = "execution failed"
	}
	for i, step := range execution.Steps {
		status := "succeeded"
		result.Steps[i] = planningstore.RunStepResult{StepID: step.StepID, Tool: step.Tool, Status: status, Replayed: step.Replayed}
	}
	return result
}

func canonicalPlan(plan agent.ActionPlan) (agent.ActionPlan, error) {
	if !utf8.ValidString(plan.Goal) {
		return agent.ActionPlan{}, ErrService
	}
	for _, reference := range plan.PolicyRefs {
		if !utf8.ValidString(reference) {
			return agent.ActionPlan{}, ErrService
		}
	}
	for _, step := range plan.Steps {
		if !utf8.ValidString(step.ID) || !utf8.ValidString(step.Tool) || !utf8.ValidString(step.SuccessCondition) || !utf8.Valid(step.Arguments) {
			return agent.ActionPlan{}, ErrService
		}
		for _, dependency := range step.DependsOn {
			if !utf8.ValidString(dependency) {
				return agent.ActionPlan{}, ErrService
			}
		}
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return agent.ActionPlan{}, ErrService
	}
	decoded, err := agent.DecodeActionPlan(data)
	if err != nil {
		return agent.ActionPlan{}, ErrService
	}
	return decoded, nil
}

func validEvidence(evidence []policy.Evidence) bool {
	for _, item := range evidence {
		values := []string{item.ChunkID, item.CitationID, item.PolicyID, item.Version, item.Section, item.Region, item.ProductCategory, item.Text}
		for _, value := range values {
			if !utf8.ValidString(value) {
				return false
			}
		}
	}
	return true
}
