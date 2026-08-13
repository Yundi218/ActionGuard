package mcpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type GetOrderParams struct {
	OrderID string `json:"order_id" jsonschema:"Order identifier"`
}

type GetShipmentParams struct {
	OrderID string `json:"order_id"`
}

type CheckInventoryParams struct {
	SKU string `json:"sku"`
}

type CheckEligibilityParams struct {
	OrderID string `json:"order_id"`
}

type CreateReturnParams struct {
	OrderID string `json:"order_id"`
	Reason  string `json:"reason"`
}

type CreateReplacementParams struct {
	OrderID string `json:"order_id"`
	SKU     string `json:"sku"`
	Reason  string `json:"reason"`
}

type IssueRefundParams struct {
	OrderID     string `json:"order_id"`
	AmountCents int64  `json:"amount_cents"`
}

type IssueCouponParams struct {
	AmountCents int64  `json:"amount_cents"`
	Reason      string `json:"reason"`
}

type trustedShipment struct {
	ID        string    `json:"id"`
	OrderID   string    `json:"order_id"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

func TrustedContextMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		meta := toolkit.CallContext{
			RunID:          r.Header.Get("X-ActionGuard-Run"),
			StepID:         r.Header.Get("X-ActionGuard-Step"),
			UserID:         r.Header.Get("X-ActionGuard-User"),
			Scopes:         strings.Fields(r.Header.Get("X-ActionGuard-Scopes")),
			IdempotencyKey: r.Header.Get("Idempotency-Key"),
		}
		next.ServeHTTP(w, r.WithContext(toolkit.WithCallContext(r.Context(), meta)))
	})
}

func getOrderHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[GetOrderParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params GetOrderParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		order, err := svc.GetOrder(ctx, meta.UserID, params.OrderID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.Order]{Trusted: order})
	}
}

func getShipmentHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[GetShipmentParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params GetShipmentParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		shipment, err := svc.GetShipment(ctx, meta.UserID, params.OrderID)
		if err != nil {
			return nil, nil, err
		}
		trusted := trustedShipment{
			ID: shipment.ID, OrderID: shipment.OrderID, Status: shipment.Status, UpdatedAt: shipment.UpdatedAt,
		}
		return jsonResult(toolkit.Envelope[trustedShipment]{
			Trusted:       trusted,
			UntrustedText: map[string]string{"untrusted_note": shipment.UntrustedNote},
		})
	}
}

func checkInventoryHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[CheckInventoryParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params CheckInventoryParams) (*mcp.CallToolResult, any, error) {
		_, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		inventory, err := svc.CheckInventory(ctx, params.SKU)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.Inventory]{Trusted: inventory})
	}
}

func checkEligibilityHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[CheckEligibilityParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params CheckEligibilityParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		eligibility, err := svc.CheckEligibility(ctx, meta.UserID, params.OrderID)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.Eligibility]{Trusted: eligibility})
	}
}

func createReturnHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[CreateReturnParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params CreateReturnParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		result, err := svc.CreateReturn(ctx, meta.UserID, params.OrderID, params.Reason, meta.IdempotencyKey)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.WriteResult]{Trusted: result, Replayed: result.Replayed})
	}
}

func createReplacementHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[CreateReplacementParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params CreateReplacementParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		result, err := svc.CreateReplacement(ctx, meta.UserID, params.OrderID, params.SKU, params.Reason, meta.IdempotencyKey)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.WriteResult]{Trusted: result, Replayed: result.Replayed})
	}
}

func issueRefundHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[IssueRefundParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params IssueRefundParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		result, err := svc.IssueRefund(ctx, meta.UserID, params.OrderID, params.AmountCents, meta.IdempotencyKey)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.WriteResult]{Trusted: result, Replayed: result.Replayed})
	}
}

func issueCouponHandler(svc *commerce.Service, contract toolContract) mcp.ToolHandlerFor[IssueCouponParams, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, params IssueCouponParams) (*mcp.CallToolResult, any, error) {
		meta, err := validateCall(ctx, contract)
		if err != nil {
			return nil, nil, err
		}
		result, err := svc.IssueCoupon(ctx, meta.UserID, params.AmountCents, params.Reason, meta.IdempotencyKey)
		if err != nil {
			return nil, nil, err
		}
		return jsonResult(toolkit.Envelope[commerce.WriteResult]{Trusted: result, Replayed: result.Replayed})
	}
}

func validateCall(ctx context.Context, contract toolContract) (toolkit.CallContext, error) {
	meta, err := toolkit.FromContext(ctx)
	if err != nil {
		return toolkit.CallContext{}, commerce.ErrInvalidToolContext
	}
	if err := meta.Validate(contract.Risk); err != nil {
		if contract.Risk != toolkit.Read && meta.IdempotencyKey == "" {
			return toolkit.CallContext{}, commerce.ErrIdempotencyKey
		}
		return toolkit.CallContext{}, commerce.ErrInvalidToolContext
	}
	if !meta.HasScope(contract.Scope) {
		return toolkit.CallContext{}, commerce.ErrForbidden
	}
	return meta, nil
}

func jsonResult[T any](envelope toolkit.Envelope[T]) (*mcp.CallToolResult, any, error) {
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
}
