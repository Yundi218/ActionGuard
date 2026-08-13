package commerce

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/database"
)

func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := database.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		truncate table idempotency_records, coupons, refunds, replacements, returns,
		shipments, orders, inventory, products, users cascade
	`); err != nil {
		t.Fatal(err)
	}

	deliveredAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`insert into users (id, display_name) values ($1, $2)`, []any{"user_018", "Ari Example"}},
		{`insert into products (sku, name) values ($1, $2)`, []any{"SKU-RED-42", "Red Widget"}},
		{`insert into inventory (sku, available, reserved) values ($1, $2, $3)`, []any{"SKU-RED-42", 4, 0}},
		{`insert into orders (id, user_id, sku, status, paid_amount_cents, delivered_at) values ($1, $2, $3, $4, $5, $6)`, []any{"AG-1042", "user_018", "SKU-RED-42", "delivered", int64(12900), deliveredAt}},
		{`insert into shipments (id, order_id, status, untrusted_note) values ($1, $2, $3, $4)`, []any{"SHIP-1042", "AG-1042", "delivered", "Left with concierge"}},
	} {
		if _, err := pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	return NewPostgresStore(pool)
}

func TestPostgresStoreGetOrder(t *testing.T) {
	store := newTestStore(t)

	order, err := store.GetOrder(context.Background(), "AG-1042")
	if err != nil || order.UserID != "user_018" || order.PaidAmountCents != 12900 {
		t.Fatalf("order = %#v, err = %v", order, err)
	}
}

func TestPostgresStoreReadsShipmentAndInventory(t *testing.T) {
	store := newTestStore(t)

	shipment, err := store.GetShipmentByOrder(context.Background(), "AG-1042")
	if err != nil || shipment.UntrustedNote != "Left with concierge" {
		t.Fatalf("shipment = %#v, err = %v", shipment, err)
	}

	inventory, err := store.GetInventory(context.Background(), "SKU-RED-42")
	if err != nil || inventory.Available != 4 || inventory.Reserved != 0 {
		t.Fatalf("inventory = %#v, err = %v", inventory, err)
	}
}

func TestPostgresStoreCreateReturnIsIdempotent(t *testing.T) {
	store := newTestStore(t)

	first, err := store.CreateReturn(context.Background(), "AG-1042", "damaged", "return-key-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateReturn(context.Background(), "AG-1042", "damaged", "return-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.ResourceID != second.ResourceID || !second.Replayed {
		t.Fatalf("first/second = %#v/%#v", first, second)
	}
}

func TestPostgresStoreCreateReturnRejectsEmptyIdempotencyKey(t *testing.T) {
	store := newTestStore(t)

	_, err := store.CreateReturn(context.Background(), "AG-1042", "damaged", "")
	if !errors.Is(err, ErrIdempotencyKey) {
		t.Fatalf("err = %v", err)
	}
}

func TestPostgresStoreWritesPrioritizeEmptyIdempotencyKey(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "replacement",
			call: func() error {
				_, err := store.CreateReplacement(ctx, "AG-1042", "SKU-RED-42", "damaged", "")
				return err
			},
		},
		{
			name: "refund",
			call: func() error {
				_, err := store.IssueRefund(ctx, "AG-1042", 0, "")
				return err
			},
		},
		{
			name: "coupon",
			call: func() error {
				_, err := store.IssueCoupon(ctx, "user_018", 0, "service recovery", "")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrIdempotencyKey) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPostgresStoreCreateReturnSerializesConcurrentSameKey(t *testing.T) {
	store := newTestStore(t)

	start := make(chan struct{})
	results := make(chan WriteResult, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := store.CreateReturn(context.Background(), "AG-1042", "damaged", "return-key-concurrent")
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var got []WriteResult
	for result := range results {
		got = append(got, result)
	}
	if len(got) != 2 || got[0].ResourceID == "" || got[0].ResourceID != got[1].ResourceID {
		t.Fatalf("results = %#v", got)
	}
	if got[0].Replayed == got[1].Replayed {
		t.Fatalf("expected exactly one replay, results = %#v", got)
	}

	var count int
	if err := store.pool.QueryRow(context.Background(), `select count(*) from returns`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("return count = %d", count)
	}
}

func TestPostgresStoreCreateReplacementReservesInventory(t *testing.T) {
	store := newTestStore(t)

	result, err := store.CreateReplacement(context.Background(), "AG-1042", "SKU-RED-42", "damaged", "replacement-key-1")
	if err != nil || result.ResourceType != "replacement" || result.Status != "created" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}

	inventory, err := store.GetInventory(context.Background(), "SKU-RED-42")
	if err != nil || inventory.Available != 3 || inventory.Reserved != 1 {
		t.Fatalf("inventory = %#v, err = %v", inventory, err)
	}
}

func TestPostgresStoreIssueRefundUpdatesOrderBalance(t *testing.T) {
	store := newTestStore(t)

	result, err := store.IssueRefund(context.Background(), "AG-1042", 800, "refund-key-1")
	if err != nil || result.ResourceType != "refund" || result.Status != "created" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}

	order, err := store.GetOrder(context.Background(), "AG-1042")
	if err != nil || order.RefundedAmountCents != 800 {
		t.Fatalf("order = %#v, err = %v", order, err)
	}
}

func TestPostgresStoreIssueCouponCreatesCoupon(t *testing.T) {
	store := newTestStore(t)

	result, err := store.IssueCoupon(context.Background(), "user_018", 1200, "service recovery", "coupon-key-1")
	if err != nil || result.ResourceType != "coupon" || result.Status != "created" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}
