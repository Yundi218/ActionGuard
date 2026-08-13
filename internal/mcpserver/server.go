package mcpserver

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/internal/toolkit"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolContract = toolkit.Contract

type toolRegistration func(*mcp.Server, *commerce.Service, toolContract, *slog.Logger)

type Option func(*serverOptions)

type serverOptions struct {
	logger *slog.Logger
}

func WithLogger(logger *slog.Logger) Option {
	return func(options *serverOptions) {
		options.logger = logger
	}
}

type commerceToolCatalogEntry struct {
	toolContract
	register toolRegistration
}

var toolRegistrations = map[string]toolRegistration{
	"get_order":          typedToolRegistration(getOrderHandler),
	"get_shipment":       typedToolRegistration(getShipmentHandler),
	"check_inventory":    typedToolRegistration(checkInventoryHandler),
	"check_eligibility":  typedToolRegistration(checkEligibilityHandler),
	"create_return":      typedToolRegistration(createReturnHandler),
	"create_replacement": typedToolRegistration(createReplacementHandler),
	"issue_refund":       typedToolRegistration(issueRefundHandler),
	"issue_coupon":       typedToolRegistration(issueCouponHandler),
}

var commerceToolCatalog = buildCommerceToolCatalog()

func buildCommerceToolCatalog() []commerceToolCatalogEntry {
	contracts := toolkit.Registry()
	catalog := make([]commerceToolCatalogEntry, 0, len(contracts))
	for _, contract := range contracts {
		catalog = append(catalog, commerceToolCatalogEntry{
			toolContract: contract,
			register:     toolRegistrations[contract.Name],
		})
	}
	return catalog
}

func New(svc *commerce.Service, options ...Option) *mcp.Server {
	config := serverOptions{logger: slog.Default()}
	for _, option := range options {
		option(&config)
	}
	if config.logger == nil {
		config.logger = slog.Default()
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "actionguard-commerce", Version: "v0.1.0"}, nil)
	for _, tool := range commerceToolCatalog {
		tool.register(server, svc, tool.toolContract, config.logger)
	}
	return server
}

func typedToolRegistration[Params any](handlerFactory func(*commerce.Service, toolContract) mcp.ToolHandlerFor[Params, any]) toolRegistration {
	return func(server *mcp.Server, svc *commerce.Service, contract toolContract, logger *slog.Logger) {
		handler := handlerFactory(svc, contract)
		mcp.AddTool(
			server,
			&mcp.Tool{Name: contract.Name, Description: contract.Description, InputSchema: contract.InputSchema},
			func(ctx context.Context, request *mcp.CallToolRequest, params Params) (*mcp.CallToolResult, any, error) {
				result, output, err := handler(ctx, request, params)
				if err != nil {
					return nil, nil, publicToolError(ctx, logger, contract.Name, err)
				}
				return result, output, nil
			},
		)
	}
}

func publicToolError(ctx context.Context, logger *slog.Logger, toolName string, err error) error {
	for _, public := range []error{
		commerce.ErrNotFound,
		commerce.ErrForbidden,
		commerce.ErrIneligible,
		commerce.ErrInventoryEmpty,
		commerce.ErrInvalidAmount,
		commerce.ErrIdempotencyKey,
		commerce.ErrIdempotencyConflict,
		commerce.ErrInvalidToolContext,
	} {
		if errors.Is(err, public) {
			return public
		}
	}
	logger.ErrorContext(ctx, "commerce MCP tool failed", "tool", toolName, "error", err)
	return commerce.ErrInternalTool
}
