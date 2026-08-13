package querycontext

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/Yundi218/ActionGuard/internal/commerce"
)

var (
	ErrNeedsInput     = errors.New("order ID required")
	ErrAmbiguousOrder = errors.New("multiple order IDs found")
)

var orderIDPattern = regexp.MustCompile(`\bAG-[0-9]{4,}\b`)

type Context struct {
	OrderID         string
	Region          string
	ProductCategory string
	At              time.Time
}

type OrderContextReader interface {
	ResolveOrderContext(context.Context, string, string) (commerce.OrderContext, error)
}

type Resolver struct {
	orders OrderContextReader
	now    func() time.Time
}

func NewResolver(orders OrderContextReader) *Resolver {
	return NewResolverWithClock(orders, time.Now)
}

func NewResolverWithClock(orders OrderContextReader, now func() time.Time) *Resolver {
	return &Resolver{orders: orders, now: now}
}

func (r *Resolver) Resolve(ctx context.Context, userID, region, message string) (Context, error) {
	orderID := ""
	for _, candidate := range orderIDPattern.FindAllString(message, -1) {
		if orderID == "" {
			orderID = candidate
			continue
		}
		if candidate != orderID {
			return Context{}, ErrAmbiguousOrder
		}
	}
	if orderID == "" {
		return Context{}, ErrNeedsInput
	}

	orderContext, err := r.orders.ResolveOrderContext(ctx, userID, orderID)
	if err != nil {
		return Context{}, err
	}
	return Context{
		OrderID:         orderContext.Order.ID,
		Region:          region,
		ProductCategory: orderContext.ProductCategory,
		At:              r.now().UTC(),
	}, nil
}
