package commerce

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	createReturnOperation      = "create_return"
	createReplacementOperation = "create_replacement"
	issueRefundOperation       = "issue_refund"
	issueCouponOperation       = "issue_coupon"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) GetOrder(ctx context.Context, id string) (Order, error) {
	var order Order
	err := s.pool.QueryRow(ctx, `
		select id, user_id, sku, status, paid_amount_cents, refunded_amount_cents, delivered_at
		from orders
		where id = $1
	`, id).Scan(
		&order.ID,
		&order.UserID,
		&order.SKU,
		&order.Status,
		&order.PaidAmountCents,
		&order.RefundedAmountCents,
		&order.DeliveredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	return order, err
}

func (s *PostgresStore) GetShipmentByOrder(ctx context.Context, orderID string) (Shipment, error) {
	var shipment Shipment
	err := s.pool.QueryRow(ctx, `
		select id, order_id, status, untrusted_note, updated_at
		from shipments
		where order_id = $1
	`, orderID).Scan(
		&shipment.ID,
		&shipment.OrderID,
		&shipment.Status,
		&shipment.UntrustedNote,
		&shipment.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Shipment{}, ErrNotFound
	}
	return shipment, err
}

func (s *PostgresStore) GetInventory(ctx context.Context, sku string) (Inventory, error) {
	var inventory Inventory
	err := s.pool.QueryRow(ctx, `
		select sku, available, reserved
		from inventory
		where sku = $1
	`, sku).Scan(&inventory.SKU, &inventory.Available, &inventory.Reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return Inventory{}, ErrNotFound
	}
	return inventory, err
}

func (s *PostgresStore) ReplayWrite(ctx context.Context, identity IdempotencyIdentity) (result WriteResult, replayed bool, err error) {
	if err := validateIdentity(identity); err != nil {
		return WriteResult{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteResult{}, false, err
	}
	defer captureRollbackError(ctx, tx.Rollback, &err)

	if err := lockIdempotencyIdentity(ctx, tx, identity); err != nil {
		return WriteResult{}, false, err
	}
	result, replayed, err = replayedResult(ctx, tx, identity)
	if err != nil {
		return WriteResult{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResult{}, false, err
	}
	return result, replayed, nil
}

func (s *PostgresStore) CreateReturn(ctx context.Context, identity IdempotencyIdentity, orderID, reason string, evaluatedAt time.Time) (WriteResult, error) {
	return s.write(ctx, identity, createReturnOperation, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		order, err := lockOwnedOrder(ctx, tx, identity.PrincipalID, orderID)
		if err != nil {
			return WriteResult{}, err
		}
		if !eligibilityForOrder(order, evaluatedAt).Eligible {
			return WriteResult{}, ErrIneligible
		}

		var id string
		err = tx.QueryRow(ctx, `
			insert into returns (order_id, reason, status)
			values ($1, $2, 'created')
			returning id::text
		`, orderID, reason).Scan(&id)
		if err != nil {
			return WriteResult{}, mapForeignKeyError(err)
		}
		return createdResult("return", id), nil
	})
}

func (s *PostgresStore) CreateReplacement(ctx context.Context, identity IdempotencyIdentity, orderID, sku, reason string, evaluatedAt time.Time) (WriteResult, error) {
	return s.write(ctx, identity, createReplacementOperation, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		order, err := lockOwnedOrder(ctx, tx, identity.PrincipalID, orderID)
		if err != nil {
			return WriteResult{}, err
		}
		if !eligibilityForOrder(order, evaluatedAt).Eligible {
			return WriteResult{}, ErrIneligible
		}

		var available int
		err = tx.QueryRow(ctx, `
			select available
			from inventory
			where sku = $1
			for update
		`, sku).Scan(&available)
		if errors.Is(err, pgx.ErrNoRows) {
			return WriteResult{}, ErrNotFound
		}
		if err != nil {
			return WriteResult{}, err
		}
		if available <= 0 {
			return WriteResult{}, ErrInventoryEmpty
		}

		var id string
		err = tx.QueryRow(ctx, `
			insert into replacements (order_id, sku, reason, status)
			values ($1, $2, $3, 'created')
			returning id::text
		`, orderID, sku, reason).Scan(&id)
		if err != nil {
			return WriteResult{}, mapForeignKeyError(err)
		}
		if _, err := tx.Exec(ctx, `
			update inventory
			set available = available - 1, reserved = reserved + 1
			where sku = $1
		`, sku); err != nil {
			return WriteResult{}, mapInventoryError(err)
		}
		return createdResult("replacement", id), nil
	})
}

func (s *PostgresStore) IssueRefund(ctx context.Context, identity IdempotencyIdentity, orderID string, amountCents int64) (WriteResult, error) {
	return s.write(ctx, identity, issueRefundOperation, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		order, err := lockOwnedOrder(ctx, tx, identity.PrincipalID, orderID)
		if err != nil {
			return WriteResult{}, err
		}
		if amountCents <= 0 || amountCents > order.PaidAmountCents-order.RefundedAmountCents {
			return WriteResult{}, ErrInvalidAmount
		}

		var id string
		err = tx.QueryRow(ctx, `
			insert into refunds (order_id, amount_cents, status)
			values ($1, $2, 'created')
			returning id::text
		`, orderID, amountCents).Scan(&id)
		if err != nil {
			return WriteResult{}, mapRefundError(err)
		}
		if _, err := tx.Exec(ctx, `
			update orders
			set refunded_amount_cents = refunded_amount_cents + $2
			where id = $1
		`, orderID, amountCents); err != nil {
			return WriteResult{}, mapRefundError(err)
		}
		return createdResult("refund", id), nil
	})
}

func (s *PostgresStore) IssueCoupon(ctx context.Context, identity IdempotencyIdentity, amountCents int64, reason string) (WriteResult, error) {
	return s.write(ctx, identity, issueCouponOperation, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		if amountCents <= 0 {
			return WriteResult{}, ErrInvalidAmount
		}

		var id string
		err := tx.QueryRow(ctx, `
			insert into coupons (user_id, amount_cents, reason)
			values ($1, $2, $3)
			returning id::text
		`, identity.PrincipalID, amountCents, reason).Scan(&id)
		if err != nil {
			return WriteResult{}, mapCouponError(err)
		}
		return createdResult("coupon", id), nil
	})
}

func (s *PostgresStore) write(ctx context.Context, identity IdempotencyIdentity, operation string, create func(context.Context, pgx.Tx) (WriteResult, error)) (result WriteResult, err error) {
	if err := identity.validate(operation); err != nil {
		return WriteResult{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	defer captureRollbackError(ctx, tx.Rollback, &err)

	if err := lockIdempotencyIdentity(ctx, tx, identity); err != nil {
		return WriteResult{}, err
	}
	result, replayed, err := replayedResult(ctx, tx, identity)
	if err != nil {
		return WriteResult{}, err
	}
	if replayed {
		if err := tx.Commit(ctx); err != nil {
			return WriteResult{}, err
		}
		return result, nil
	}

	result, err = create(ctx, tx)
	if err != nil {
		return WriteResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into idempotency_records (
			operation, idempotency_key, principal_id, request_fingerprint, result_type, result_id
		)
		values ($1, $2, $3, $4, $5, $6)
	`, identity.Operation, identity.Key, identity.PrincipalID, identity.RequestFingerprint, result.ResourceType, result.ResourceID); err != nil {
		return WriteResult{}, mapIdempotencyInsertError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResult{}, err
	}
	return result, nil
}

func lockIdempotencyIdentity(ctx context.Context, tx pgx.Tx, identity IdempotencyIdentity) error {
	_, err := tx.Exec(ctx, `
		select pg_advisory_xact_lock(hashtextextended($1, hashtextextended($2, 0)))
	`, identity.Operation, identity.Key)
	return err
}

func replayedResult(ctx context.Context, tx pgx.Tx, identity IdempotencyIdentity) (WriteResult, bool, error) {
	var principalID, fingerprint, resultType, resultID string
	err := tx.QueryRow(ctx, `
		select principal_id, request_fingerprint, result_type, result_id
		from idempotency_records
		where operation = $1 and idempotency_key = $2
	`, identity.Operation, identity.Key).Scan(&principalID, &fingerprint, &resultType, &resultID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteResult{}, false, nil
	}
	if err != nil {
		return WriteResult{}, false, err
	}
	if principalID != identity.PrincipalID || fingerprint != identity.RequestFingerprint {
		return WriteResult{}, false, ErrIdempotencyConflict
	}
	return WriteResult{
		ResourceType: resultType,
		ResourceID:   resultID,
		Status:       "created",
		Replayed:     true,
	}, true, nil
}

func lockOwnedOrder(ctx context.Context, tx pgx.Tx, principalID, orderID string) (Order, error) {
	var order Order
	err := tx.QueryRow(ctx, `
		select id, user_id, sku, status, paid_amount_cents, refunded_amount_cents, delivered_at
		from orders
		where id = $1
		for update
	`, orderID).Scan(
		&order.ID,
		&order.UserID,
		&order.SKU,
		&order.Status,
		&order.PaidAmountCents,
		&order.RefundedAmountCents,
		&order.DeliveredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, err
	}
	if order.UserID != principalID {
		return Order{}, ErrForbidden
	}
	return order, nil
}

func validateIdentity(identity IdempotencyIdentity) error {
	switch identity.Operation {
	case createReturnOperation, createReplacementOperation, issueRefundOperation, issueCouponOperation:
		return identity.validate(identity.Operation)
	default:
		return ErrInternalTool
	}
}

func mapForeignKeyError(err error) error {
	if postgresErrorCode(err) == "23503" {
		return ErrNotFound
	}
	return err
}

func mapInventoryError(err error) error {
	if postgresErrorCode(err) == "23514" {
		return ErrInventoryEmpty
	}
	return mapForeignKeyError(err)
}

func mapRefundError(err error) error {
	if postgresErrorCode(err) == "23514" {
		return ErrInvalidAmount
	}
	return mapForeignKeyError(err)
}

func mapCouponError(err error) error {
	if postgresErrorCode(err) == "23514" {
		return ErrInvalidAmount
	}
	return mapForeignKeyError(err)
}

func mapIdempotencyInsertError(err error) error {
	if postgresErrorCode(err) == "23505" {
		return ErrIdempotencyConflict
	}
	return err
}

func postgresErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func captureRollbackError(ctx context.Context, rollback func(context.Context) error, err *error) {
	rollbackErr := rollback(ctx)
	if rollbackErr == nil || errors.Is(rollbackErr, pgx.ErrTxClosed) {
		return
	}
	wrapped := fmt.Errorf("rollback transaction: %w", rollbackErr)
	if *err == nil {
		*err = wrapped
		return
	}
	*err = errors.Join(*err, wrapped)
}

func createdResult(resourceType, resourceID string) WriteResult {
	return WriteResult{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Status:       "created",
	}
}
