package commerce

import "context"

type Store interface {
	GetOrder(context.Context, string) (Order, error)
	GetShipmentByOrder(context.Context, string) (Shipment, error)
	GetInventory(context.Context, string) (Inventory, error)
	CreateReturn(context.Context, string, string, string) (WriteResult, error)
	CreateReplacement(context.Context, string, string, string, string) (WriteResult, error)
	IssueRefund(context.Context, string, int64, string) (WriteResult, error)
	IssueCoupon(context.Context, string, int64, string, string) (WriteResult, error)
}
