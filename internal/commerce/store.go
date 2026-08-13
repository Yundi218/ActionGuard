package commerce

import (
	"context"
	"time"
)

type Store interface {
	GetOrder(context.Context, string) (Order, error)
	GetProductCategory(context.Context, string) (string, error)
	GetShipmentByOrder(context.Context, string) (Shipment, error)
	GetInventory(context.Context, string) (Inventory, error)
	ReplayWrite(context.Context, IdempotencyIdentity) (WriteResult, bool, error)
	CreateReturn(context.Context, IdempotencyIdentity, string, string, time.Time) (WriteResult, error)
	CreateReplacement(context.Context, IdempotencyIdentity, string, string, string, time.Time) (WriteResult, error)
	IssueRefund(context.Context, IdempotencyIdentity, string, int64) (WriteResult, error)
	IssueCoupon(context.Context, IdempotencyIdentity, int64, string) (WriteResult, error)
}
