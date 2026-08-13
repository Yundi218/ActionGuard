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
	if order.Status != "delivered" || order.DeliveredAt == nil {
		return Eligibility{ReasonCode: "not_delivered"}, nil
	}

	deadline := order.DeliveredAt.Add(returnWindow)
	if s.now().After(deadline) {
		return Eligibility{ReasonCode: "return_window_expired", Deadline: &deadline}, nil
	}
	return Eligibility{Eligible: true, ReasonCode: "within_return_window", Deadline: &deadline}, nil
}

func (s *Service) CreateReturn(ctx context.Context, userID, orderID, reason, idempotencyKey string) (WriteResult, error) {
	eligibility, err := s.CheckEligibility(ctx, userID, orderID)
	if err != nil {
		return WriteResult{}, err
	}
	if !eligibility.Eligible {
		return WriteResult{}, ErrIneligible
	}
	return s.store.CreateReturn(ctx, orderID, reason, idempotencyKey)
}

func (s *Service) CreateReplacement(ctx context.Context, userID, orderID, sku, reason, idempotencyKey string) (WriteResult, error) {
	eligibility, err := s.CheckEligibility(ctx, userID, orderID)
	if err != nil {
		return WriteResult{}, err
	}
	if !eligibility.Eligible {
		return WriteResult{}, ErrIneligible
	}

	inventory, err := s.CheckInventory(ctx, sku)
	if err != nil {
		return WriteResult{}, err
	}
	if inventory.Available <= 0 {
		return WriteResult{}, ErrInventoryEmpty
	}
	return s.store.CreateReplacement(ctx, orderID, sku, reason, idempotencyKey)
}

func (s *Service) IssueRefund(ctx context.Context, userID, orderID string, amountCents int64, idempotencyKey string) (WriteResult, error) {
	order, err := s.ownedOrder(ctx, userID, orderID)
	if err != nil {
		return WriteResult{}, err
	}
	if amountCents <= 0 || amountCents > order.PaidAmountCents-order.RefundedAmountCents {
		return WriteResult{}, ErrInvalidAmount
	}
	return s.store.IssueRefund(ctx, orderID, amountCents, idempotencyKey)
}

func (s *Service) IssueCoupon(ctx context.Context, userID string, amountCents int64, reason, idempotencyKey string) (WriteResult, error) {
	if amountCents < 1 || amountCents > 2000 {
		return WriteResult{}, ErrInvalidAmount
	}
	return s.store.IssueCoupon(ctx, userID, amountCents, reason, idempotencyKey)
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
