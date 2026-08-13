package commerce

import (
	"context"
	"time"
)

const returnWindow = 30 * 24 * time.Hour

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return NewServiceWithClock(store, time.Now)
}

func NewServiceWithClock(store Store, now func() time.Time) *Service {
	return &Service{store: store, now: now}
}

func (s *Service) GetOrder(ctx context.Context, userID, orderID string) (Order, error) {
	return s.ownedOrder(ctx, userID, orderID)
}

func (s *Service) GetShipment(ctx context.Context, userID, orderID string) (Shipment, error) {
	if _, err := s.ownedOrder(ctx, userID, orderID); err != nil {
		return Shipment{}, err
	}
	return s.store.GetShipmentByOrder(ctx, orderID)
}

func (s *Service) CheckInventory(ctx context.Context, sku string) (Inventory, error) {
	return s.store.GetInventory(ctx, sku)
}

func (s *Service) CheckEligibility(ctx context.Context, userID, orderID string) (Eligibility, error) {
	order, err := s.ownedOrder(ctx, userID, orderID)
	if err != nil {
		return Eligibility{}, err
	}
	return eligibilityForOrder(order, s.now()), nil
}

func (s *Service) CreateReturn(ctx context.Context, userID, orderID, reason, idempotencyKey string) (WriteResult, error) {
	identity, err := newReturnIdentity(userID, orderID, reason, idempotencyKey)
	if err != nil {
		return WriteResult{}, err
	}
	if result, replayed, err := s.store.ReplayWrite(ctx, identity); err != nil || replayed {
		return result, err
	}

	evaluatedAt := s.now()
	order, err := s.ownedOrder(ctx, userID, orderID)
	if err != nil {
		return s.replayOrError(ctx, identity, err)
	}
	if !eligibilityForOrder(order, evaluatedAt).Eligible {
		return s.replayOrError(ctx, identity, ErrIneligible)
	}
	return s.store.CreateReturn(ctx, identity, orderID, reason, evaluatedAt)
}

func (s *Service) CreateReplacement(ctx context.Context, userID, orderID, sku, reason, idempotencyKey string) (WriteResult, error) {
	identity, err := newReplacementIdentity(userID, orderID, sku, reason, idempotencyKey)
	if err != nil {
		return WriteResult{}, err
	}
	if result, replayed, err := s.store.ReplayWrite(ctx, identity); err != nil || replayed {
		return result, err
	}

	evaluatedAt := s.now()
	order, err := s.ownedOrder(ctx, userID, orderID)
	if err != nil {
		return s.replayOrError(ctx, identity, err)
	}
	if !eligibilityForOrder(order, evaluatedAt).Eligible {
		return s.replayOrError(ctx, identity, ErrIneligible)
	}
	inventory, err := s.CheckInventory(ctx, sku)
	if err != nil {
		return s.replayOrError(ctx, identity, err)
	}
	if inventory.Available <= 0 {
		return s.replayOrError(ctx, identity, ErrInventoryEmpty)
	}
	return s.store.CreateReplacement(ctx, identity, orderID, sku, reason, evaluatedAt)
}

func (s *Service) IssueRefund(ctx context.Context, userID, orderID string, amountCents int64, idempotencyKey string) (WriteResult, error) {
	identity, err := newRefundIdentity(userID, orderID, amountCents, idempotencyKey)
	if err != nil {
		return WriteResult{}, err
	}
	if result, replayed, err := s.store.ReplayWrite(ctx, identity); err != nil || replayed {
		return result, err
	}

	order, err := s.ownedOrder(ctx, userID, orderID)
	if err != nil {
		return s.replayOrError(ctx, identity, err)
	}
	if amountCents <= 0 || amountCents > order.PaidAmountCents-order.RefundedAmountCents {
		return s.replayOrError(ctx, identity, ErrInvalidAmount)
	}
	return s.store.IssueRefund(ctx, identity, orderID, amountCents)
}

func (s *Service) IssueCoupon(ctx context.Context, userID string, amountCents int64, reason, idempotencyKey string) (WriteResult, error) {
	identity, err := newCouponIdentity(userID, amountCents, reason, idempotencyKey)
	if err != nil {
		return WriteResult{}, err
	}
	if result, replayed, err := s.store.ReplayWrite(ctx, identity); err != nil || replayed {
		return result, err
	}
	if amountCents < 1 || amountCents > 2000 {
		return s.replayOrError(ctx, identity, ErrInvalidAmount)
	}
	return s.store.IssueCoupon(ctx, identity, amountCents, reason)
}

func (s *Service) replayOrError(ctx context.Context, identity IdempotencyIdentity, original error) (WriteResult, error) {
	result, replayed, err := s.store.ReplayWrite(ctx, identity)
	if err != nil {
		return WriteResult{}, err
	}
	if replayed {
		return result, nil
	}
	return WriteResult{}, original
}

func (s *Service) ownedOrder(ctx context.Context, userID, orderID string) (Order, error) {
	order, err := s.store.GetOrder(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if order.UserID != userID {
		return Order{}, ErrForbidden
	}
	return order, nil
}

func eligibilityForOrder(order Order, now time.Time) Eligibility {
	if order.Status != "delivered" || order.DeliveredAt == nil {
		return Eligibility{ReasonCode: "not_delivered"}
	}
	deadline := order.DeliveredAt.Add(returnWindow)
	if now.After(deadline) {
		return Eligibility{ReasonCode: "return_window_expired", Deadline: &deadline}
	}
	return Eligibility{Eligible: true, ReasonCode: "within_return_window", Deadline: &deadline}
}
