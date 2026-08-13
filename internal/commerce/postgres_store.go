package commerce

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
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

func (s *PostgresStore) CreateReturn(ctx context.Context, orderID, reason, idempotencyKey string) (WriteResult, error) {
	return s.write(ctx, createReturnOperation, idempotencyKey, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		if err := lockOrder(ctx, tx, orderID); err != nil {
			return WriteResult{}, err
		}

		var id string
		err := tx.QueryRow(ctx, `
			insert into returns (order_id, reason, status)
			values ($1, $2, 'created')
			returning id::text
		`, orderID, reason).Scan(&id)
		if err != nil {
			return WriteResult{}, err
		}
		return createdResult("return", id), nil
	})
}

func (s *PostgresStore) CreateReplacement(ctx context.Context, orderID, sku, reason, idempotencyKey string) (WriteResult, error) {
	return s.write(ctx, createReplacementOperation, idempotencyKey, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		if err := lockOrder(ctx, tx, orderID); err != nil {
			return WriteResult{}, err
		}

		var available int
		err := tx.QueryRow(ctx, `
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
		if available == 0 {
			return WriteResult{}, ErrInventoryEmpty
		}

		var id string
		err = tx.QueryRow(ctx, `
			insert into replacements (order_id, sku, reason, status)
			values ($1, $2, $3, 'created')
			returning id::text
		`, orderID, sku, reason).Scan(&id)
		if err != nil {
			return WriteResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			update inventory
			set available = available - 1, reserved = reserved + 1
			where sku = $1
		`, sku); err != nil {
			return WriteResult{}, err
		}
		return createdResult("replacement", id), nil
	})
}

func (s *PostgresStore) IssueRefund(ctx context.Context, orderID string, amountCents int64, idempotencyKey string) (WriteResult, error) {
	if idempotencyKey == "" {
		return WriteResult{}, ErrIdempotencyKey
	}
	if amountCents <= 0 {
		return WriteResult{}, ErrInvalidAmount
	}

	return s.write(ctx, issueRefundOperation, idempotencyKey, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		if err := lockOrder(ctx, tx, orderID); err != nil {
			return WriteResult{}, err
		}

		var id string
		err := tx.QueryRow(ctx, `
			insert into refunds (order_id, amount_cents, status)
			values ($1, $2, 'created')
			returning id::text
		`, orderID, amountCents).Scan(&id)
		if err != nil {
			return WriteResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			update orders
			set refunded_amount_cents = refunded_amount_cents + $2
			where id = $1
		`, orderID, amountCents); err != nil {
			return WriteResult{}, err
		}
		return createdResult("refund", id), nil
	})
}

func (s *PostgresStore) IssueCoupon(ctx context.Context, userID string, amountCents int64, reason, idempotencyKey string) (WriteResult, error) {
	if idempotencyKey == "" {
		return WriteResult{}, ErrIdempotencyKey
	}
	if amountCents <= 0 {
		return WriteResult{}, ErrInvalidAmount
	}

	return s.write(ctx, issueCouponOperation, idempotencyKey, func(ctx context.Context, tx pgx.Tx) (WriteResult, error) {
		var id string
		err := tx.QueryRow(ctx, `
			insert into coupons (user_id, amount_cents, reason)
			values ($1, $2, $3)
			returning id::text
		`, userID, amountCents, reason).Scan(&id)
		if err != nil {
			return WriteResult{}, err
		}
		return createdResult("coupon", id), nil
	})
}

func (s *PostgresStore) write(ctx context.Context, operation, idempotencyKey string, create func(context.Context, pgx.Tx) (WriteResult, error)) (WriteResult, error) {
	if idempotencyKey == "" {
		return WriteResult{}, ErrIdempotencyKey
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WriteResult{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		select pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))
	`, operation, idempotencyKey); err != nil {
		return WriteResult{}, err
	}

	result, replayed, err := replayedResult(ctx, tx, operation, idempotencyKey)
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
		insert into idempotency_records (operation, idempotency_key, result_type, result_id)
		values ($1, $2, $3, $4)
	`, operation, idempotencyKey, result.ResourceType, result.ResourceID); err != nil {
		return WriteResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return WriteResult{}, err
	}
	return result, nil
}

func replayedResult(ctx context.Context, tx pgx.Tx, operation, key string) (WriteResult, bool, error) {
	var result WriteResult
	err := tx.QueryRow(ctx, `
		select result_type, result_id
		from idempotency_records
		where operation = $1 and idempotency_key = $2
	`, operation, key).Scan(&result.ResourceType, &result.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteResult{}, false, nil
	}
	if err != nil {
		return WriteResult{}, false, err
	}
	result.Status = "created"
	result.Replayed = true
	return result, true, nil
}

func lockOrder(ctx context.Context, tx pgx.Tx, orderID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		select id
		from orders
		where id = $1
		for update
	`, orderID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func createdResult(resourceType, resourceID string) WriteResult {
	return WriteResult{
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Status:       "created",
	}
}
