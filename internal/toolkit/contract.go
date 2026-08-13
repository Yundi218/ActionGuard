package toolkit

import (
	"context"
	"errors"
)

type Risk string

const (
	Read          Risk = "read"
	Write         Risk = "write"
	HighRiskWrite Risk = "high_risk_write"
)

type CallContext struct {
	RunID          string
	StepID         string
	UserID         string
	Scopes         []string
	IdempotencyKey string
}

func (c CallContext) HasScope(want string) bool {
	for _, scope := range c.Scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func (c CallContext) Validate(risk Risk) error {
	if c.RunID == "" || c.StepID == "" || c.UserID == "" {
		return errors.New("run_id, step_id, and user_id are required")
	}
	if risk != Read && c.IdempotencyKey == "" {
		return errors.New("idempotency_key is required for writes")
	}
	return nil
}

type contextKey struct{}

func WithCallContext(ctx context.Context, meta CallContext) context.Context {
	return context.WithValue(ctx, contextKey{}, meta)
}

func FromContext(ctx context.Context) (CallContext, error) {
	meta, ok := ctx.Value(contextKey{}).(CallContext)
	if !ok {
		return CallContext{}, errors.New("trusted call context is missing")
	}
	return meta, nil
}

type Envelope[T any] struct {
	Trusted       T                 `json:"trusted"`
	UntrustedText map[string]string `json:"untrusted_text,omitempty"`
	Replayed      bool              `json:"replayed"`
}
