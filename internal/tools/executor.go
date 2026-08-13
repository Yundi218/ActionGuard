package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Yundi218/ActionGuard/internal/verifier"
)

const (
	StatusSucceeded      = "succeeded"
	StatusWaitingRuntime = "waiting_runtime"
	StatusFailed         = "failed"
)

var (
	ErrInvalidExecution    = errors.New("invalid verified execution request")
	ErrToolExecution       = errors.New("tool execution failed")
	ErrInvalidToolResponse = errors.New("invalid tool response")
	ErrMCPTransport        = errors.New("MCP transport failed")
	ErrMCPResponseTooLarge = errors.New("MCP response exceeds size limit")
	ErrExecutorClosed      = errors.New("executor is closed")
	ErrMCPRedirect         = errors.New("MCP redirects are disabled")
)

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
	Status string
	Steps  []StepResult
}

type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}
