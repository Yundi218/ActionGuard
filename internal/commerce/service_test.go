package commerce

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var errStorage = errors.New("storage unavailable")

type fakeStore struct {
	order     Order
	shipment  Shipment
	inventory Inventory
	result    WriteResult
	err       error

	getOrderIDs    []string
	shipmentOrders []string
	inventorySKUs  []string
	returnCalls    []returnCall
	replaceCalls   []replacementCall
	refundCalls    []refundCall
	couponCalls    []couponCall
}

type returnCall struct {
	orderID        string
	reason         string
	idempotencyKey string
}

type replacementCall struct {
	orderID        string
	sku            string
	reason         string
	idempotencyKey string
}

type refundCall struct {
	orderID        string
	amountCents    int64
	idempotencyKey string
}

type couponCall struct {
	userID         string
	amountCents    int64
	reason         string
	idempotencyKey string
}

func (f *fakeStore) GetOrder(_ context.Context, id string) (Order, error) {
	f.getOrderIDs = append(f.getOrderIDs, id)
	if f.err != nil {
		return Order{}, fmt.Errorf("get order: %w", f.err)
	}
	return f.order, nil
}

func (f *fakeStore) GetShipmentByOrder(_ context.Context, orderID string) (Shipment, error) {
	f.shipmentOrders = append(f.shipmentOrders, orderID)
	if f.err != nil {
		return Shipment{}, fmt.Errorf("get shipment: %w", f.err)
	}
	return f.shipment, nil
}

func (f *fakeStore) GetInventory(_ context.Context, sku string) (Inventory, error) {
	f.inventorySKUs = append(f.inventorySKUs, sku)
	if f.err != nil {
		return Inventory{}, fmt.Errorf("get inventory: %w", f.err)
	}
	return f.inventory, nil
}

func (f *fakeStore) CreateReturn(_ context.Context, orderID, reason, idempotencyKey string) (WriteResult, error) {
	f.returnCalls = append(f.returnCalls, returnCall{orderID, reason, idempotencyKey})
	if f.err != nil {
		return WriteResult{}, fmt.Errorf("create return: %w", f.err)
	}
	return f.result, nil
}

func (f *fakeStore) CreateReplacement(_ context.Context, orderID, sku, reason, idempotencyKey string) (WriteResult, error) {
	f.replaceCalls = append(f.replaceCalls, replacementCall{orderID, sku, reason, idempotencyKey})
	if f.err != nil {
		return WriteResult{}, fmt.Errorf("create replacement: %w", f.err)
	}
	return f.result, nil
}

func (f *fakeStore) IssueRefund(_ context.Context, orderID string, amountCents int64, idempotencyKey string) (WriteResult, error) {
	f.refundCalls = append(f.refundCalls, refundCall{orderID, amountCents, idempotencyKey})
	if f.err != nil {
		return WriteResult{}, fmt.Errorf("issue refund: %w", f.err)
	}
	return f.result, nil
}

func (f *fakeStore) IssueCoupon(_ context.Context, userID string, amountCents int64, reason, idempotencyKey string) (WriteResult, error) {
	f.couponCalls = append(f.couponCalls, couponCall{userID, amountCents, reason, idempotencyKey})
	if f.err != nil {
		return WriteResult{}, fmt.Errorf("issue coupon: %w", f.err)
	}
	return f.result, nil
}

func ownedDeliveredOrder(deliveredAt time.Time) Order {
	return Order{
		ID:              "AG-1042",
		UserID:          "user_018",
		SKU:             "SKU-RED-42",
		Status:          "delivered",
		PaidAmountCents: 12900,
		DeliveredAt:     &deliveredAt,
	}
}

func TestServiceRejectsCrossUserOrder(t *testing.T) {
	svc := NewService(&fakeStore{order: Order{ID: "AG-1042", UserID: "user_018"}})
	_, err := svc.GetOrder(context.Background(), "user_999", "AG-1042")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
}

func TestServiceGetOrderForwardsOrderID(t *testing.T) {
	store := &fakeStore{order: Order{ID: "AG-1042", UserID: "user_018"}}
	got, err := NewService(store).GetOrder(context.Background(), "user_018", "AG-1042")
	if err != nil || got.ID != "AG-1042" || len(store.getOrderIDs) != 1 || store.getOrderIDs[0] != "AG-1042" {
		t.Fatalf("order = %#v, calls = %#v, err = %v", got, store.getOrderIDs, err)
	}
}

func TestServiceGetShipmentRequiresOrderOwnership(t *testing.T) {
	store := &fakeStore{order: Order{ID: "AG-1042", UserID: "user_018"}, shipment: Shipment{ID: "SHIP-1042"}}
	_, err := NewService(store).GetShipment(context.Background(), "user_999", "AG-1042")
	if !errors.Is(err, ErrForbidden) || len(store.shipmentOrders) != 0 {
		t.Fatalf("err = %v, shipment calls = %#v", err, store.shipmentOrders)
	}
}

func TestServiceGetShipmentForOwnedOrder(t *testing.T) {
	store := &fakeStore{order: Order{ID: "AG-1042", UserID: "user_018"}, shipment: Shipment{ID: "SHIP-1042", OrderID: "AG-1042"}}
	got, err := NewService(store).GetShipment(context.Background(), "user_018", "AG-1042")
	if err != nil || got.ID != "SHIP-1042" || len(store.shipmentOrders) != 1 || store.shipmentOrders[0] != "AG-1042" {
		t.Fatalf("shipment = %#v, calls = %#v, err = %v", got, store.shipmentOrders, err)
	}
}

func TestServiceCheckInventoryForwardsSKU(t *testing.T) {
	store := &fakeStore{inventory: Inventory{SKU: "SKU-RED-42", Available: 3}}
	got, err := NewService(store).CheckInventory(context.Background(), "SKU-RED-42")
	if err != nil || got.Available != 3 || len(store.inventorySKUs) != 1 || store.inventorySKUs[0] != "SKU-RED-42" {
		t.Fatalf("inventory = %#v, calls = %#v, err = %v", got, store.inventorySKUs, err)
	}
}

func TestEligibilityUsesDeliveredAtAndThirtyDayWindow(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	svc := NewServiceWithClock(&fakeStore{order: ownedDeliveredOrder(delivered)}, func() time.Time { return delivered.Add(29 * 24 * time.Hour) })
	result, err := svc.CheckEligibility(context.Background(), "user_018", "AG-1042")
	if err != nil || !result.Eligible || result.ReasonCode != "within_return_window" || result.Deadline == nil || !result.Deadline.Equal(delivered.Add(30*24*time.Hour)) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestServiceEligibilityAtThirtyDayDeadlineIsEligible(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	svc := NewServiceWithClock(&fakeStore{order: ownedDeliveredOrder(delivered)}, func() time.Time { return delivered.Add(30 * 24 * time.Hour) })
	result, err := svc.CheckEligibility(context.Background(), "user_018", "AG-1042")
	if err != nil || !result.Eligible || result.ReasonCode != "within_return_window" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestServiceEligibilityPastThirtyDaysIsIneligible(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	svc := NewServiceWithClock(&fakeStore{order: ownedDeliveredOrder(delivered)}, func() time.Time { return delivered.Add(30*24*time.Hour + time.Nanosecond) })
	result, err := svc.CheckEligibility(context.Background(), "user_018", "AG-1042")
	if err != nil || result.Eligible || result.ReasonCode != "return_window_expired" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestServiceEligibilityRequiresDeliveredStatusAndTimestamp(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		order Order
	}{
		{name: "not delivered", order: Order{ID: "AG-1042", UserID: "user_018", Status: "shipped", DeliveredAt: &delivered}},
		{name: "missing timestamp", order: Order{ID: "AG-1042", UserID: "user_018", Status: "delivered"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := NewServiceWithClock(&fakeStore{order: tt.order}, func() time.Time { return delivered }).CheckEligibility(context.Background(), "user_018", "AG-1042")
			if err != nil || result.Eligible || result.ReasonCode != "not_delivered" {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
		})
	}
}

func TestServiceCreateReturnRequiresEligibilityAndForwardsArguments(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{order: ownedDeliveredOrder(delivered), result: WriteResult{ResourceType: "return", ResourceID: "return-1"}}
	result, err := NewServiceWithClock(store, func() time.Time { return delivered }).CreateReturn(context.Background(), "user_018", "AG-1042", "damaged", "return-key-1")
	if err != nil || result.ResourceID != "return-1" || len(store.returnCalls) != 1 || store.returnCalls[0] != (returnCall{"AG-1042", "damaged", "return-key-1"}) {
		t.Fatalf("result = %#v, calls = %#v, err = %v", result, store.returnCalls, err)
	}
}

func TestServiceCreateReturnRejectsIneligibleOrder(t *testing.T) {
	store := &fakeStore{order: Order{ID: "AG-1042", UserID: "user_018", Status: "shipped"}}
	_, err := NewService(store).CreateReturn(context.Background(), "user_018", "AG-1042", "damaged", "return-key-1")
	if !errors.Is(err, ErrIneligible) || len(store.returnCalls) != 0 {
		t.Fatalf("err = %v, calls = %#v", err, store.returnCalls)
	}
}

func TestServiceCreateReplacementRequiresPositiveInventoryAndForwardsArguments(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		available int
		wantErr   error
		wantCalls int
	}{
		{name: "positive", available: 1, wantCalls: 1},
		{name: "zero", available: 0, wantErr: ErrInventoryEmpty},
		{name: "negative", available: -1, wantErr: ErrInventoryEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{order: ownedDeliveredOrder(delivered), inventory: Inventory{SKU: "SKU-BLUE-7", Available: tt.available}, result: WriteResult{ResourceID: "replacement-1"}}
			result, err := NewServiceWithClock(store, func() time.Time { return delivered }).CreateReplacement(context.Background(), "user_018", "AG-1042", "SKU-BLUE-7", "wrong size", "replacement-key-1")
			if !errors.Is(err, tt.wantErr) || len(store.replaceCalls) != tt.wantCalls {
				t.Fatalf("result = %#v, err = %v, calls = %#v", result, err, store.replaceCalls)
			}
			if tt.wantCalls == 1 && store.replaceCalls[0] != (replacementCall{"AG-1042", "SKU-BLUE-7", "wrong size", "replacement-key-1"}) {
				t.Fatalf("calls = %#v", store.replaceCalls)
			}
		})
	}
}

func TestServiceIssueRefundEnforcesRemainingBalanceAndForwardsArguments(t *testing.T) {
	tests := []struct {
		name      string
		amount    int64
		wantErr   error
		wantCalls int
	}{
		{name: "zero", amount: 0, wantErr: ErrInvalidAmount},
		{name: "exact remaining", amount: 10000, wantCalls: 1},
		{name: "over remaining", amount: 10001, wantErr: ErrInvalidAmount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{order: Order{ID: "AG-1042", UserID: "user_018", PaidAmountCents: 12900, RefundedAmountCents: 2900}, result: WriteResult{ResourceID: "refund-1"}}
			result, err := NewService(store).IssueRefund(context.Background(), "user_018", "AG-1042", tt.amount, "refund-key-1")
			if !errors.Is(err, tt.wantErr) || len(store.refundCalls) != tt.wantCalls {
				t.Fatalf("result = %#v, err = %v, calls = %#v", result, err, store.refundCalls)
			}
			if tt.wantCalls == 1 && store.refundCalls[0] != (refundCall{"AG-1042", 10000, "refund-key-1"}) {
				t.Fatalf("calls = %#v", store.refundCalls)
			}
		})
	}
}

func TestServiceIssueCouponEnforcesPhaseOneLimitsAndForwardsArguments(t *testing.T) {
	tests := []struct {
		name      string
		amount    int64
		wantErr   error
		wantCalls int
	}{
		{name: "minimum", amount: 1, wantCalls: 1},
		{name: "maximum", amount: 2000, wantCalls: 1},
		{name: "zero", amount: 0, wantErr: ErrInvalidAmount},
		{name: "above maximum", amount: 2001, wantErr: ErrInvalidAmount},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{result: WriteResult{ResourceID: "coupon-1"}}
			result, err := NewService(store).IssueCoupon(context.Background(), "user_018", tt.amount, "service recovery", "coupon-key-1")
			if !errors.Is(err, tt.wantErr) || len(store.couponCalls) != tt.wantCalls {
				t.Fatalf("result = %#v, err = %v, calls = %#v", result, err, store.couponCalls)
			}
			if tt.wantCalls == 1 && store.couponCalls[0] != (couponCall{"user_018", tt.amount, "service recovery", "coupon-key-1"}) {
				t.Fatalf("calls = %#v", store.couponCalls)
			}
		})
	}
}

func TestServicePreservesStorageErrors(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		call func(*Service) error
	}{
		{name: "get order", call: func(s *Service) error { _, err := s.GetOrder(context.Background(), "user_018", "AG-1042"); return err }},
		{name: "get shipment", call: func(s *Service) error {
			_, err := s.GetShipment(context.Background(), "user_018", "AG-1042")
			return err
		}},
		{name: "check inventory", call: func(s *Service) error { _, err := s.CheckInventory(context.Background(), "SKU-RED-42"); return err }},
		{name: "check eligibility", call: func(s *Service) error {
			_, err := s.CheckEligibility(context.Background(), "user_018", "AG-1042")
			return err
		}},
		{name: "create return", call: func(s *Service) error {
			_, err := s.CreateReturn(context.Background(), "user_018", "AG-1042", "damaged", "return-key")
			return err
		}},
		{name: "create replacement", call: func(s *Service) error {
			_, err := s.CreateReplacement(context.Background(), "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-key")
			return err
		}},
		{name: "issue refund", call: func(s *Service) error {
			_, err := s.IssueRefund(context.Background(), "user_018", "AG-1042", 100, "refund-key")
			return err
		}},
		{name: "issue coupon", call: func(s *Service) error {
			_, err := s.IssueCoupon(context.Background(), "user_018", 100, "service recovery", "coupon-key")
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{order: ownedDeliveredOrder(delivered), inventory: Inventory{Available: 1}, err: errStorage}
			if err := tt.call(NewServiceWithClock(store, func() time.Time { return delivered })); !errors.Is(err, errStorage) {
				t.Fatalf("err = %v, want wrapped storage error", err)
			}
		})
	}
}
