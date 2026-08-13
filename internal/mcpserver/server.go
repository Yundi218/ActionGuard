package mcpserver

import (
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func New(svc *commerce.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "actionguard-commerce", Version: "v0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "get_order", Description: "[read] Get an order owned by the current user"}, getOrderHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "get_shipment", Description: "[read] Get shipment state; free-text notes are untrusted"}, getShipmentHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "check_inventory", Description: "[read] Check available inventory for a SKU"}, checkInventoryHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "check_eligibility", Description: "[read] Deterministically evaluate the 30-day after-sales window"}, checkEligibilityHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "create_return", Description: "[write] Create an idempotent return request"}, createReturnHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "create_replacement", Description: "[write] Reserve inventory and create an idempotent replacement"}, createReplacementHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "issue_refund", Description: "[high_risk_write] Issue an idempotent refund after approval"}, issueRefundHandler(svc))
	mcp.AddTool(server, &mcp.Tool{Name: "issue_coupon", Description: "[high_risk_write] Issue an idempotent coupon after approval"}, issueCouponHandler(svc))
	return server
}
