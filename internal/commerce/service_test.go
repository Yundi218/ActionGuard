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
	order           Order
	shipment        Shipment
	inventory       Inventory
	productCategory string
	result          WriteResult

	getOrderErr        error
	shipmentErr        error
	inventoryErr       error
	productCategoryErr error
	returnErr          error
	replacementErr     error
	refundErr          error
	couponErr          error
	replayErr          error
	replayResult       WriteResult
	replayed           bool

	getOrderIDs    []string
	shipmentOrders []string
	inventorySKUs  []string
	categorySKUs   []string
	returnCalls    []returnCall
	replaceCalls   []replacementCall
	refundCalls    []refundCall
	couponCalls    []couponCall
	replayCalls    []IdempotencyIdentity

	getOrderContexts    []context.Context
	shipmentContexts    []context.Context
	inventoryContexts   []context.Context
	categoryContexts    []context.Context
	returnContexts      []context.Context
	replacementContexts []context.Context
	refundContexts      []context.Context
	couponContexts      []context.Context
	replayContexts      []context.Context
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

func (f *fakeStore) GetOrder(ctx context.Context, id string) (Order, error) {
	f.getOrderIDs = append(f.getOrderIDs, id)
	f.getOrderContexts = append(f.getOrderContexts, ctx)
	if f.getOrderErr != nil {
		return Order{}, fmt.Errorf("get order: %w", f.getOrderErr)
	}
	return f.order, nil
}

func (f *fakeStore) GetShipmentByOrder(ctx context.Context, orderID string) (Shipment, error) {
	f.shipmentOrders = append(f.shipmentOrders, orderID)
	f.shipmentContexts = append(f.shipmentContexts, ctx)
	if f.shipmentErr != nil {
		return Shipment{}, fmt.Errorf("get shipment: %w", f.shipmentErr)
	}
	return f.shipment, nil
}

func (f *fakeStore) GetInventory(ctx context.Context, sku string) (Inventory, error) {
	f.inventorySKUs = append(f.inventorySKUs, sku)
	f.inventoryContexts = append(f.inventoryContexts, ctx)
	if f.inventoryErr != nil {
		return Inventory{}, fmt.Errorf("get inventory: %w", f.inventoryErr)
	}
	return f.inventory, nil
}

func (f *fakeStore) GetProductCategory(ctx context.Context, sku string) (string, error) {
	f.categorySKUs = append(f.categorySKUs, sku)
	f.categoryContexts = append(f.categoryContexts, ctx)
	if f.productCategoryErr != nil {
		return "", fmt.Errorf("get product category: %w", f.productCategoryErr)
	}
	return f.productCategory, nil
}

func (f *fakeStore) ReplayWrite(ctx context.Context, identity IdempotencyIdentity) (WriteResult, bool, error) {
	f.replayCalls = append(f.replayCalls, identity)
	f.replayContexts = append(f.replayContexts, ctx)
	if f.replayErr != nil {
		return WriteResult{}, false, fmt.Errorf("replay write: %w", f.replayErr)
	}
	return f.replayResult, f.replayed, nil
}

func (f *fakeStore) CreateReturn(ctx context.Context, identity IdempotencyIdentity, orderID, reason string, _ time.Time) (WriteResult, error) {
	f.returnCalls = append(f.returnCalls, returnCall{orderID, reason, identity.Key})
	f.returnContexts = append(f.returnContexts, ctx)
	if f.returnErr != nil {
		return WriteResult{}, fmt.Errorf("create return: %w", f.returnErr)
	}
	return f.result, nil
}

func (f *fakeStore) CreateReplacement(ctx context.Context, identity IdempotencyIdentity, orderID, sku, reason string, _ time.Time) (WriteResult, error) {
	f.replaceCalls = append(f.replaceCalls, replacementCall{orderID, sku, reason, identity.Key})
	f.replacementContexts = append(f.replacementContexts, ctx)
	if f.replacementErr != nil {
		return WriteResult{}, fmt.Errorf("create replacement: %w", f.replacementErr)
	}
	return f.result, nil
}

func (f *fakeStore) IssueRefund(ctx context.Context, identity IdempotencyIdentity, orderID string, amountCents int64) (WriteResult, error) {
	f.refundCalls = append(f.refundCalls, refundCall{orderID, amountCents, identity.Key})
	f.refundContexts = append(f.refundContexts, ctx)
	if f.refundErr != nil {
		return WriteResult{}, fmt.Errorf("issue refund: %w", f.refundErr)
	}
	return f.result, nil
}

func (f *fakeStore) IssueCoupon(ctx context.Context, identity IdempotencyIdentity, amountCents int64, reason string) (WriteResult, error) {
	f.couponCalls = append(f.couponCalls, couponCall{identity.PrincipalID, amountCents, reason, identity.Key})
	f.couponContexts = append(f.couponContexts, ctx)
	if f.couponErr != nil {
		return WriteResult{}, fmt.Errorf("issue coupon: %w", f.couponErr)
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

func TestServiceResolveOrderContextChecksOwnershipBeforeProductCategory(t *testing.T) {
	store := &fakeStore{
		order:           Order{ID: "AG-1042", UserID: "user_018", SKU: "SKU-RED-42"},
		productCategory: "electronics",
	}

	got, err := NewService(store).ResolveOrderContext(context.Background(), "user_999", "AG-1042")
	if got != (OrderContext{}) || !errors.Is(err, ErrForbidden) {
		t.Fatalf("context = %#v, err = %v, want empty context and ErrForbidden", got, err)
	}
	if len(store.categorySKUs) != 0 {
		t.Fatalf("category lookup occurred before ownership check: %v", store.categorySKUs)
	}
}

func TestServiceResolveOrderContextReturnsOwnedOrderAndProductCategory(t *testing.T) {
	ctx := context.WithValue(context.Background(), struct{}{}, "request-context")
	order := Order{ID: "AG-1042", UserID: "user_018", SKU: "SKU-RED-42"}
	store := &fakeStore{order: order, productCategory: "electronics"}

	got, err := NewService(store).ResolveOrderContext(ctx, "user_018", "AG-1042")
	if err != nil || got != (OrderContext{Order: order, ProductCategory: "electronics"}) {
		t.Fatalf("context = %#v, err = %v", got, err)
	}
	if fmt.Sprint(store.getOrderIDs) != fmt.Sprint([]string{"AG-1042"}) || fmt.Sprint(store.categorySKUs) != fmt.Sprint([]string{"SKU-RED-42"}) {
		t.Fatalf("order/category calls = %v/%v", store.getOrderIDs, store.categorySKUs)
	}
	if len(store.categoryContexts) != 1 || store.categoryContexts[0] != ctx {
		t.Fatalf("category contexts = %#v, want original context", store.categoryContexts)
	}
}

func TestServiceResolveOrderContextPreservesStableProductError(t *testing.T) {
	store := &fakeStore{
		order:              Order{ID: "AG-1042", UserID: "user_018", SKU: "SKU-MISSING"},
		productCategoryErr: ErrNotFound,
	}

	got, err := NewService(store).ResolveOrderContext(context.Background(), "user_018", "AG-1042")
	if got != (OrderContext{}) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("context = %#v, err = %v, want empty context and ErrNotFound", got, err)
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

func TestServiceCreateReplacementRejectsIneligibleOrderWithoutCheckingInventoryOrWriting(t *testing.T) {
	store := &fakeStore{order: Order{ID: "AG-1042", UserID: "user_018", Status: "shipped"}}
	_, err := NewService(store).CreateReplacement(context.Background(), "user_018", "AG-1042", "SKU-BLUE-7", "wrong size", "replacement-key-1")
	if !errors.Is(err, ErrIneligible) || len(store.inventorySKUs) != 0 || len(store.replaceCalls) != 0 {
		t.Fatalf("err = %v, inventory calls = %#v, replacement calls = %#v", err, store.inventorySKUs, store.replaceCalls)
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

func TestServiceExactReplayPrecedesMutablePreconditions(t *testing.T) {
	want := WriteResult{ResourceType: "existing", ResourceID: "existing-id", Status: "created", Replayed: true}
	for _, tt := range []struct {
		name string
		call func(*Service) (WriteResult, error)
	}{
		{name: "return", call: func(s *Service) (WriteResult, error) {
			return s.CreateReturn(context.Background(), "user_018", "AG-1042", "damaged", "replay-key")
		}},
		{name: "replacement", call: func(s *Service) (WriteResult, error) {
			return s.CreateReplacement(context.Background(), "user_018", "AG-1042", "SKU-RED-42", "damaged", "replay-key")
		}},
		{name: "refund", call: func(s *Service) (WriteResult, error) {
			return s.IssueRefund(context.Background(), "user_018", "AG-1042", 12900, "replay-key")
		}},
		{name: "coupon", call: func(s *Service) (WriteResult, error) {
			return s.IssueCoupon(context.Background(), "user_018", 2000, "service recovery", "replay-key")
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{replayResult: want, replayed: true}
			got, err := tt.call(NewService(store))
			if err != nil || got != want {
				t.Fatalf("result/error = %#v/%v, want %#v/nil", got, err, want)
			}
			if len(store.replayCalls) != 1 || len(store.getOrderIDs) != 0 || len(store.inventorySKUs) != 0 || len(store.returnCalls) != 0 || len(store.replaceCalls) != 0 || len(store.refundCalls) != 0 || len(store.couponCalls) != 0 {
				t.Fatalf("store calls after replay: replay=%d orders=%v inventory=%v writes=%d/%d/%d/%d", len(store.replayCalls), store.getOrderIDs, store.inventorySKUs, len(store.returnCalls), len(store.replaceCalls), len(store.refundCalls), len(store.couponCalls))
			}
		})
	}
}

func TestServiceIdempotencyConflictPrecedesOwnershipChecks(t *testing.T) {
	store := &fakeStore{replayErr: ErrIdempotencyConflict}
	result, err := NewService(store).CreateReturn(context.Background(), "other-user", "AG-1042", "damaged", "conflict-key")
	if !errors.Is(err, ErrIdempotencyConflict) || result != (WriteResult{}) {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if len(store.getOrderIDs) != 0 || len(store.returnCalls) != 0 {
		t.Fatalf("conflict reached ownership or write: orders=%v writes=%v", store.getOrderIDs, store.returnCalls)
	}
}

func TestServicePropagatesErrorsFromReachedStoreMethods(t *testing.T) {
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name  string
		store *fakeStore
		call  func(*Service) error
	}{
		{name: "get order", store: &fakeStore{getOrderErr: errStorage}, call: func(s *Service) error { _, err := s.GetOrder(context.Background(), "user_018", "AG-1042"); return err }},
		{name: "get shipment", store: &fakeStore{shipmentErr: errStorage}, call: func(s *Service) error {
			_, err := s.GetShipment(context.Background(), "user_018", "AG-1042")
			return err
		}},
		{name: "check inventory", store: &fakeStore{inventoryErr: errStorage}, call: func(s *Service) error { _, err := s.CheckInventory(context.Background(), "SKU-RED-42"); return err }},
		{name: "check eligibility", store: &fakeStore{getOrderErr: errStorage}, call: func(s *Service) error {
			_, err := s.CheckEligibility(context.Background(), "user_018", "AG-1042")
			return err
		}},
		{name: "create return", store: &fakeStore{returnErr: errStorage}, call: func(s *Service) error {
			_, err := s.CreateReturn(context.Background(), "user_018", "AG-1042", "damaged", "return-key")
			return err
		}},
		{name: "create replacement inventory", store: &fakeStore{inventoryErr: errStorage}, call: func(s *Service) error {
			_, err := s.CreateReplacement(context.Background(), "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-key")
			return err
		}},
		{name: "create replacement write", store: &fakeStore{replacementErr: errStorage}, call: func(s *Service) error {
			_, err := s.CreateReplacement(context.Background(), "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-key")
			return err
		}},
		{name: "issue refund", store: &fakeStore{refundErr: errStorage}, call: func(s *Service) error {
			_, err := s.IssueRefund(context.Background(), "user_018", "AG-1042", 100, "refund-key")
			return err
		}},
		{name: "issue coupon", store: &fakeStore{couponErr: errStorage}, call: func(s *Service) error {
			_, err := s.IssueCoupon(context.Background(), "user_018", 100, "service recovery", "coupon-key")
			return err
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := tt.store
			store.order = ownedDeliveredOrder(delivered)
			store.inventory = Inventory{Available: 1}
			if err := tt.call(NewServiceWithClock(store, func() time.Time { return delivered })); !errors.Is(err, errStorage) {
				t.Fatalf("err = %v, want wrapped storage error", err)
			}
		})
	}
}

func TestServiceForwardsContextToEveryReachedStoreMethod(t *testing.T) {
	type contextKey string
	ctx := context.WithValue(context.Background(), contextKey("request"), "request-1")
	delivered := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name     string
		call     func(*Service) error
		contexts func(*fakeStore) []context.Context
	}{
		{name: "get order", call: func(s *Service) error { _, err := s.GetOrder(ctx, "user_018", "AG-1042"); return err }, contexts: func(f *fakeStore) []context.Context { return f.getOrderContexts }},
		{name: "get shipment", call: func(s *Service) error { _, err := s.GetShipment(ctx, "user_018", "AG-1042"); return err }, contexts: func(f *fakeStore) []context.Context { return append(f.getOrderContexts, f.shipmentContexts...) }},
		{name: "get inventory", call: func(s *Service) error { _, err := s.CheckInventory(ctx, "SKU-RED-42"); return err }, contexts: func(f *fakeStore) []context.Context { return f.inventoryContexts }},
		{name: "check eligibility", call: func(s *Service) error { _, err := s.CheckEligibility(ctx, "user_018", "AG-1042"); return err }, contexts: func(f *fakeStore) []context.Context { return f.getOrderContexts }},
		{name: "create return", call: func(s *Service) error {
			_, err := s.CreateReturn(ctx, "user_018", "AG-1042", "damaged", "return-key")
			return err
		}, contexts: func(f *fakeStore) []context.Context { return append(f.getOrderContexts, f.returnContexts...) }},
		{name: "create replacement", call: func(s *Service) error {
			_, err := s.CreateReplacement(ctx, "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-key")
			return err
		}, contexts: func(f *fakeStore) []context.Context {
			return append(append(f.getOrderContexts, f.inventoryContexts...), f.replacementContexts...)
		}},
		{name: "issue refund", call: func(s *Service) error {
			_, err := s.IssueRefund(ctx, "user_018", "AG-1042", 100, "refund-key")
			return err
		}, contexts: func(f *fakeStore) []context.Context { return append(f.getOrderContexts, f.refundContexts...) }},
		{name: "issue coupon", call: func(s *Service) error {
			_, err := s.IssueCoupon(ctx, "user_018", 100, "service recovery", "coupon-key")
			return err
		}, contexts: func(f *fakeStore) []context.Context { return f.couponContexts }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{order: ownedDeliveredOrder(delivered), inventory: Inventory{Available: 1}}
			if err := tt.call(NewServiceWithClock(store, func() time.Time { return delivered })); err != nil {
				t.Fatal(err)
			}
			gotContexts := tt.contexts(store)
			if len(gotContexts) == 0 {
				t.Fatal("store method was not called")
			}
			for _, got := range gotContexts {
				if got != ctx {
					t.Fatalf("context = %#v, want %#v", got, ctx)
				}
			}
		})
	}
}
