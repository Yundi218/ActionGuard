package mcpserver

import (
	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolContract struct {
	Name        string
	Description string
	Risk        toolkit.Risk
	Scope       string
}

var (
	getOrderTool = toolContract{
		Name: "get_order", Description: "[read] Get an order owned by the current user", Risk: toolkit.Read, Scope: "order:read",
	}
	getShipmentTool = toolContract{
		Name: "get_shipment", Description: "[read] Get shipment state; free-text notes are untrusted", Risk: toolkit.Read, Scope: "shipment:read",
	}
	checkInventoryTool = toolContract{
		Name: "check_inventory", Description: "[read] Check available inventory for a SKU", Risk: toolkit.Read, Scope: "inventory:read",
	}
	checkEligibilityTool = toolContract{
		Name: "check_eligibility", Description: "[read] Deterministically evaluate the 30-day after-sales window", Risk: toolkit.Read, Scope: "eligibility:read",
	}
	createReturnTool = toolContract{
		Name: "create_return", Description: "[write] Create an idempotent return request", Risk: toolkit.Write, Scope: "return:write",
	}
	createReplacementTool = toolContract{
		Name: "create_replacement", Description: "[write] Reserve inventory and create an idempotent replacement", Risk: toolkit.Write, Scope: "replacement:write",
	}
	issueRefundTool = toolContract{
		Name: "issue_refund", Description: "[high_risk_write] Issue an idempotent refund after approval", Risk: toolkit.HighRiskWrite, Scope: "refund:write",
	}
	issueCouponTool = toolContract{
		Name: "issue_coupon", Description: "[high_risk_write] Issue an idempotent coupon after approval", Risk: toolkit.HighRiskWrite, Scope: "coupon:write",
	}

	toolContracts = []toolContract{
		getOrderTool,
		getShipmentTool,
		checkInventoryTool,
		checkEligibilityTool,
		createReturnTool,
		createReplacementTool,
		issueRefundTool,
		issueCouponTool,
	}
)

func New(svc *commerce.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "actionguard-commerce", Version: "v0.1.0"}, nil)
	registerTool(server, getOrderTool, getOrderHandler(svc))
	registerTool(server, getShipmentTool, getShipmentHandler(svc))
	registerTool(server, checkInventoryTool, checkInventoryHandler(svc))
	registerTool(server, checkEligibilityTool, checkEligibilityHandler(svc))
	registerTool(server, createReturnTool, createReturnHandler(svc))
	registerTool(server, createReplacementTool, createReplacementHandler(svc))
	registerTool(server, issueRefundTool, issueRefundHandler(svc))
	registerTool(server, issueCouponTool, issueCouponHandler(svc))
	return server
}

func registerTool[Params any](server *mcp.Server, contract toolContract, handler mcp.ToolHandlerFor[Params, any]) {
	mcp.AddTool(server, &mcp.Tool{Name: contract.Name, Description: contract.Description}, handler)
}
