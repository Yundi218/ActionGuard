package toolkit

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrUntrustedCallContextJSON = errors.New("trusted call context cannot be decoded from JSON")

type Risk string

const (
	Read          Risk = "read"
	Write         Risk = "write"
	HighRiskWrite Risk = "high_risk_write"
)

type Contract struct {
	Name                   string
	Description            string
	Risk                   Risk
	Scope                  string
	InputSchema            json.RawMessage
	RequiresIdempotencyKey bool
}

var registry = []Contract{
	{Name: "get_order", Description: "[read] Get an order owned by the current user", Risk: Read, Scope: "order:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"description":"Order identifier","type":"string"}},"required":["order_id"],"type":"object"}`)},
	{Name: "get_shipment", Description: "[read] Get shipment state; free-text notes are untrusted", Risk: Read, Scope: "shipment:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"}},"required":["order_id"],"type":"object"}`)},
	{Name: "check_inventory", Description: "[read] Check available inventory for a SKU", Risk: Read, Scope: "inventory:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"sku":{"type":"string"}},"required":["sku"],"type":"object"}`)},
	{Name: "check_eligibility", Description: "[read] Deterministically evaluate the 30-day after-sales window", Risk: Read, Scope: "eligibility:read", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"}},"required":["order_id"],"type":"object"}`)},
	{Name: "create_return", Description: "[write] Create an idempotent return request", Risk: Write, Scope: "return:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"},"reason":{"type":"string"}},"required":["order_id","reason"],"type":"object"}`), RequiresIdempotencyKey: true},
	{Name: "create_replacement", Description: "[write] Reserve inventory and create an idempotent replacement", Risk: Write, Scope: "replacement:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"order_id":{"type":"string"},"reason":{"type":"string"},"sku":{"type":"string"}},"required":["order_id","sku","reason"],"type":"object"}`), RequiresIdempotencyKey: true},
	{Name: "issue_refund", Description: "[high_risk_write] Issue an idempotent refund after approval", Risk: HighRiskWrite, Scope: "refund:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"amount_cents":{"type":"integer"},"order_id":{"type":"string"}},"required":["order_id","amount_cents"],"type":"object"}`), RequiresIdempotencyKey: true},
	{Name: "issue_coupon", Description: "[high_risk_write] Issue an idempotent coupon after approval", Risk: HighRiskWrite, Scope: "coupon:write", InputSchema: json.RawMessage(`{"additionalProperties":false,"properties":{"amount_cents":{"type":"integer"},"reason":{"type":"string"}},"required":["amount_cents","reason"],"type":"object"}`), RequiresIdempotencyKey: true},
}

func Registry() []Contract {
	contracts := make([]Contract, len(registry))
	for index, contract := range registry {
		contracts[index] = cloneContract(contract)
	}
	return contracts
}

func LookupContract(name string) (Contract, bool) {
	for _, contract := range registry {
		if contract.Name == name {
			return cloneContract(contract), true
		}
	}
	return Contract{}, false
}

func cloneContract(contract Contract) Contract {
	contract.InputSchema = append(json.RawMessage(nil), contract.InputSchema...)
	return contract
}

func (risk Risk) Valid() bool {
	return risk == Read || risk == Write || risk == HighRiskWrite
}

type CallContext struct {
	RunID          string
	StepID         string
	UserID         string
	Scopes         []string
	IdempotencyKey string
}

// UnmarshalJSON prevents trusted metadata from being populated by tool arguments.
func (*CallContext) UnmarshalJSON([]byte) error {
	return ErrUntrustedCallContextJSON
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
