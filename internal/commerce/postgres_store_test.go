package commerce

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/database"
	"github.com/Yundi218/ActionGuard/internal/testdatabase"
)

var postgresStoreTestTime = time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

func postgresStoreTestIdentity(operation, key string) IdempotencyIdentity {
	return IdempotencyIdentity{
		Operation:          operation,
		Key:                key,
		PrincipalID:        "user_018",
		RequestFingerprint: strings.Repeat("a", 64),
	}
}

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
	lockCtx, cancelLock := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLock()
	lock, err := testdatabase.AcquirePublicSchemaLock(lockCtx, pool)
	if err != nil {
		t.Fatalf("acquire public-schema test lock: %v", err)
	}
	t.Cleanup(func() {
		releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelRelease()
		if err := lock.Release(releaseCtx); err != nil {
			t.Errorf("release public-schema test lock: %v", err)
		}
	})

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

	identity := postgresStoreTestIdentity(createReturnOperation, "return-key-1")
	first, err := store.CreateReturn(context.Background(), identity, "AG-1042", "damaged", postgresStoreTestTime)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateReturn(context.Background(), identity, "AG-1042", "damaged", postgresStoreTestTime)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || !second.Replayed {
		t.Fatalf("first/second = %#v/%#v", first, second)
	}
	second.Replayed = false
	if first != second {
		t.Fatalf("replayed result differs from original: first/second = %#v/%#v", first, second)
	}
}

func TestPostgresStoreCreateReturnRejectsEmptyIdempotencyKey(t *testing.T) {
	store := newTestStore(t)

	_, err := store.CreateReturn(context.Background(), postgresStoreTestIdentity(createReturnOperation, ""), "AG-1042", "damaged", postgresStoreTestTime)
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
				_, err := store.CreateReplacement(ctx, postgresStoreTestIdentity(createReplacementOperation, ""), "AG-1042", "SKU-RED-42", "damaged", postgresStoreTestTime)
				return err
			},
		},
		{
			name: "refund",
			call: func() error {
				_, err := store.IssueRefund(ctx, postgresStoreTestIdentity(issueRefundOperation, ""), "AG-1042", 0)
				return err
			},
		},
		{
			name: "coupon",
			call: func() error {
				_, err := store.IssueCoupon(ctx, postgresStoreTestIdentity(issueCouponOperation, ""), 0, "service recovery")
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
			result, err := store.CreateReturn(context.Background(), postgresStoreTestIdentity(createReturnOperation, "return-key-concurrent"), "AG-1042", "damaged", postgresStoreTestTime)
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

func TestPostgresStoreCreateReturnWaitsForSameIdempotencyLock(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	const operation = createReturnOperation
	const key = "return-key-lock-held"

	holder, err := store.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()

	lockTx, err := holder.Conn().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = lockTx.Rollback(ctx)
		}
	}()

	if _, err := lockTx.Exec(ctx, `
		select pg_advisory_xact_lock(hashtextextended($1, hashtextextended($2, 0)))
	`, operation, key); err != nil {
		t.Fatal(err)
	}

	var classID, objectID int64
	var objectSubID int32
	if err := lockTx.QueryRow(ctx, `
		select classid::bigint, objid::bigint, objsubid
		from pg_locks
		where pid = pg_backend_pid()
		  and locktype = 'advisory'
		  and granted
		limit 1
	`).Scan(&classID, &objectID, &objectSubID); err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		result WriteResult
		err    error
	}
	started := make(chan struct{})
	done := make(chan outcome, 1)
	go func() {
		close(started)
		result, err := store.CreateReturn(ctx, postgresStoreTestIdentity(createReturnOperation, key), "AG-1042", "damaged", postgresStoreTestTime)
		done <- outcome{result: result, err: err}
	}()
	<-started

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		err := store.pool.QueryRow(ctx, `
			select count(*)
			from pg_locks
			where locktype = 'advisory'
			  and not granted
			  and classid = $1
			  and objid = $2
			  and objsubid = $3
		`, classID, objectID, objectSubID).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 1 {
			break
		}

		select {
		case <-deadline.C:
			t.Fatal("store write did not wait for the held advisory lock")
		case <-time.After(10 * time.Millisecond):
		}
	}

	select {
	case result := <-done:
		t.Fatalf("store write completed before the advisory lock was released: %#v", result)
	default:
	}

	if err := lockTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	committed = true

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.result.ResourceType != "return" || result.result.ResourceID == "" || result.result.Replayed {
			t.Fatalf("result = %#v", result.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("store write did not complete after the advisory lock was released")
	}
}

func TestPostgresStoreCreateReplacementReservesInventory(t *testing.T) {
	store := newTestStore(t)

	result, err := store.CreateReplacement(context.Background(), postgresStoreTestIdentity(createReplacementOperation, "replacement-key-1"), "AG-1042", "SKU-RED-42", "damaged", postgresStoreTestTime)
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

	result, err := store.IssueRefund(context.Background(), postgresStoreTestIdentity(issueRefundOperation, "refund-key-1"), "AG-1042", 800)
	if err != nil || result.ResourceType != "refund" || result.Status != "created" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}

	order, err := store.GetOrder(context.Background(), "AG-1042")
	if err != nil || order.RefundedAmountCents != 800 {
		t.Fatalf("order = %#v, err = %v", order, err)
	}
}

func TestPostgresStoreIssueRefundRollsBackAfterOverRefund(t *testing.T) {
	store := newTestStore(t)
	const key = "refund-key-over-limit"

	_, err := store.IssueRefund(context.Background(), postgresStoreTestIdentity(issueRefundOperation, key), "AG-1042", 13000)
	if err == nil {
		t.Fatal("IssueRefund returned nil error for an over-refund")
	}

	var refundCount int
	if err := store.pool.QueryRow(context.Background(), `select count(*) from refunds`).Scan(&refundCount); err != nil {
		t.Fatal(err)
	}
	if refundCount != 0 {
		t.Fatalf("refund count = %d, want 0", refundCount)
	}

	var idempotencyCount int
	if err := store.pool.QueryRow(context.Background(), `
		select count(*)
		from idempotency_records
		where operation = $1 and idempotency_key = $2
	`, issueRefundOperation, key).Scan(&idempotencyCount); err != nil {
		t.Fatal(err)
	}
	if idempotencyCount != 0 {
		t.Fatalf("idempotency record count = %d, want 0", idempotencyCount)
	}

	order, err := store.GetOrder(context.Background(), "AG-1042")
	if err != nil {
		t.Fatal(err)
	}
	if order.RefundedAmountCents != 0 {
		t.Fatalf("refunded amount = %d, want 0", order.RefundedAmountCents)
	}
}

func TestPostgresStoreIssueCouponCreatesCoupon(t *testing.T) {
	store := newTestStore(t)

	result, err := store.IssueCoupon(context.Background(), postgresStoreTestIdentity(issueCouponOperation, "coupon-key-1"), 1200, "service recovery")
	if err != nil || result.ResourceType != "coupon" || result.Status != "created" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestPostgresStoreServiceReplaysReplacementAfterLastUnit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `update inventory set available = 1 where sku = 'SKU-RED-42'`); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	svc := NewServiceWithClock(store, func() time.Time { return now })
	first, err := svc.CreateReplacement(ctx, "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-last-unit")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateReplacement(ctx, "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-last-unit")
	if err != nil {
		t.Fatal(err)
	}
	if first.ResourceID == "" || second.ResourceID != first.ResourceID || !second.Replayed {
		t.Fatalf("first/second = %#v/%#v", first, second)
	}

	var replacements, available, reserved int
	if err := store.pool.QueryRow(ctx, `select count(*) from replacements`).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select available, reserved from inventory where sku = 'SKU-RED-42'`).Scan(&available, &reserved); err != nil {
		t.Fatal(err)
	}
	if replacements != 1 || available != 0 || reserved != 1 {
		t.Fatalf("database state: replacements=%d available=%d reserved=%d", replacements, available, reserved)
	}
}

func TestPostgresStoreServiceSerializesConcurrentSameKeyReplacementAtInventoryBoundary(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `update inventory set available = 1 where sku = 'SKU-RED-42'`); err != nil {
		t.Fatal(err)
	}

	svc := NewServiceWithClock(store, func() time.Time { return postgresStoreTestTime })
	type outcome struct {
		result WriteResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := svc.CreateReplacement(callCtx, "user_018", "AG-1042", "SKU-RED-42", "damaged", "replacement-same-key-concurrent")
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	var results []WriteResult
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		results = append(results, outcome.result)
	}
	if len(results) != 2 || results[0].ResourceID == "" || results[0].ResourceID != results[1].ResourceID {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Replayed == results[1].Replayed {
		t.Fatalf("expected exactly one replay, results = %#v", results)
	}

	var replacements, available, reserved int
	if err := store.pool.QueryRow(ctx, `select count(*) from replacements`).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select available, reserved from inventory where sku = 'SKU-RED-42'`).Scan(&available, &reserved); err != nil {
		t.Fatal(err)
	}
	if replacements != 1 || available != 0 || reserved != 1 {
		t.Fatalf("database state: replacements=%d available=%d reserved=%d", replacements, available, reserved)
	}
}

func TestPostgresStoreServiceReplaysFullRefund(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	svc := NewService(store)

	first, err := svc.IssueRefund(ctx, "user_018", "AG-1042", 12900, "refund-full-balance")
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.IssueRefund(ctx, "user_018", "AG-1042", 12900, "refund-full-balance")
	if err != nil {
		t.Fatal(err)
	}
	if first.ResourceID == "" || second.ResourceID != first.ResourceID || !second.Replayed {
		t.Fatalf("first/second = %#v/%#v", first, second)
	}

	var refunds int
	var refunded int64
	if err := store.pool.QueryRow(ctx, `select count(*) from refunds`).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select refunded_amount_cents from orders where id = 'AG-1042'`).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || refunded != 12900 {
		t.Fatalf("database state: refunds=%d refunded=%d", refunds, refunded)
	}
}

func TestPostgresStoreServiceReplaysExpiredAfterSalesWrites(t *testing.T) {
	for _, tt := range []struct {
		name     string
		write    func(context.Context, *Service, string) (WriteResult, error)
		countSQL string
		wantType string
	}{
		{
			name: "return",
			write: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.CreateReturn(ctx, "user_018", "AG-1042", "damaged", key)
			},
			countSQL: `select count(*) from returns`,
			wantType: "return",
		},
		{
			name: "replacement",
			write: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.CreateReplacement(ctx, "user_018", "AG-1042", "SKU-RED-42", "damaged", key)
			},
			countSQL: `select count(*) from replacements`,
			wantType: "replacement",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			svc := NewServiceWithClock(store, func() time.Time { return now })

			first, err := tt.write(ctx, svc, "expired-replay-"+tt.name)
			if err != nil {
				t.Fatal(err)
			}
			now = time.Date(2026, 9, 1, 12, 0, 0, 1, time.UTC)
			second, err := tt.write(ctx, svc, "expired-replay-"+tt.name)
			if err != nil {
				t.Fatal(err)
			}
			if first.ResourceType != tt.wantType || first.ResourceID == "" || second.ResourceID != first.ResourceID || !second.Replayed {
				t.Fatalf("first/second = %#v/%#v", first, second)
			}

			var count int
			if err := store.pool.QueryRow(ctx, tt.countSQL).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("%s count = %d, want 1", tt.wantType, count)
			}
		})
	}
}

func TestPostgresStoreConcurrentDifferentKeyRefundsRespectLockedBalance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, key := range []string{"refund-concurrent-a", "refund-concurrent-b"} {
		key := key
		go func() {
			<-start
			_, err := store.IssueRefund(ctx, postgresStoreTestIdentity(issueRefundOperation, key), "AG-1042", 8000)
			errs <- err
		}()
	}
	close(start)

	var successes, invalidAmounts int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrInvalidAmount):
			invalidAmounts++
		default:
			t.Fatalf("unexpected refund error: %v", err)
		}
	}
	if successes != 1 || invalidAmounts != 1 {
		t.Fatalf("successes=%d invalid_amounts=%d", successes, invalidAmounts)
	}

	var refunds, idempotency int
	var refunded int64
	if err := store.pool.QueryRow(ctx, `select count(*) from refunds`).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select count(*) from idempotency_records where operation = 'issue_refund'`).Scan(&idempotency); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select refunded_amount_cents from orders where id = 'AG-1042'`).Scan(&refunded); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || idempotency != 1 || refunded != 8000 {
		t.Fatalf("database state: refunds=%d idempotency=%d refunded=%d", refunds, idempotency, refunded)
	}
}

func TestPostgresStoreConcurrentDifferentKeyReplacementsRespectLockedInventory(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.pool.Exec(ctx, `update inventory set available = 1 where sku = 'SKU-RED-42'`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, key := range []string{"replacement-concurrent-a", "replacement-concurrent-b"} {
		key := key
		go func() {
			<-start
			_, err := store.CreateReplacement(ctx, postgresStoreTestIdentity(createReplacementOperation, key), "AG-1042", "SKU-RED-42", "damaged", postgresStoreTestTime)
			errs <- err
		}()
	}
	close(start)

	var successes, emptyInventory int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrInventoryEmpty):
			emptyInventory++
		default:
			t.Fatalf("unexpected replacement error: %v", err)
		}
	}
	if successes != 1 || emptyInventory != 1 {
		t.Fatalf("successes=%d empty_inventory=%d", successes, emptyInventory)
	}

	var replacements, idempotency, available, reserved int
	if err := store.pool.QueryRow(ctx, `select count(*) from replacements`).Scan(&replacements); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select count(*) from idempotency_records where operation = 'create_replacement'`).Scan(&idempotency); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `select available, reserved from inventory where sku = 'SKU-RED-42'`).Scan(&available, &reserved); err != nil {
		t.Fatal(err)
	}
	if replacements != 1 || idempotency != 1 || available != 0 || reserved != 1 {
		t.Fatalf("database state: replacements=%d idempotency=%d available=%d reserved=%d", replacements, idempotency, available, reserved)
	}
}

func TestPostgresStoreIdempotencyConflictsDoNotLeakOrMutate(t *testing.T) {
	for _, tt := range []struct {
		name     string
		first    func(context.Context, *Service, string) (WriteResult, error)
		conflict func(context.Context, *Service, string) (WriteResult, error)
		countSQL string
	}{
		{
			name: "return changed reason",
			first: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.CreateReturn(ctx, "user_018", "AG-1042", "damaged", key)
			},
			conflict: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.CreateReturn(ctx, "user_018", "AG-1042", "wrong item", key)
			},
			countSQL: `select count(*) from returns`,
		},
		{
			name: "replacement changed reason",
			first: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.CreateReplacement(ctx, "user_018", "AG-1042", "SKU-RED-42", "damaged", key)
			},
			conflict: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.CreateReplacement(ctx, "user_018", "AG-1042", "SKU-RED-42", "wrong item", key)
			},
			countSQL: `select count(*) from replacements`,
		},
		{
			name: "refund changed amount",
			first: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.IssueRefund(ctx, "user_018", "AG-1042", 800, key)
			},
			conflict: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.IssueRefund(ctx, "user_018", "AG-1042", 900, key)
			},
			countSQL: `select count(*) from refunds`,
		},
		{
			name: "coupon changed reason",
			first: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.IssueCoupon(ctx, "user_018", 1200, "service recovery", key)
			},
			conflict: func(ctx context.Context, svc *Service, key string) (WriteResult, error) {
				return svc.IssueCoupon(ctx, "user_018", 1200, "different reason", key)
			},
			countSQL: `select count(*) from coupons`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			ctx := context.Background()
			now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
			svc := NewServiceWithClock(store, func() time.Time { return now })
			key := "argument-conflict-" + tt.name

			first, err := tt.first(ctx, svc, key)
			if err != nil {
				t.Fatal(err)
			}
			conflict, err := tt.conflict(ctx, svc, key)
			if !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("conflict result/error = %#v/%v, want ErrIdempotencyConflict", conflict, err)
			}
			if conflict != (WriteResult{}) {
				t.Fatalf("conflict leaked result %#v; original resource was %q", conflict, first.ResourceID)
			}

			var count int
			if err := store.pool.QueryRow(ctx, tt.countSQL).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("side-effect count = %d, want 1", count)
			}
		})
	}
}

func TestPostgresStoreIdempotencyConflictsAcrossPrincipalsDoNotLeakOrMutate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	svc := NewService(store)
	const key = "coupon-principal-conflict"

	first, err := svc.IssueCoupon(ctx, "user_018", 1200, "service recovery", key)
	if err != nil {
		t.Fatal(err)
	}
	conflict, err := svc.IssueCoupon(ctx, "user_999", 1200, "service recovery", key)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict result/error = %#v/%v, want ErrIdempotencyConflict", conflict, err)
	}
	if conflict != (WriteResult{}) {
		t.Fatalf("conflict leaked result %#v; original resource was %q", conflict, first.ResourceID)
	}

	var coupons int
	if err := store.pool.QueryRow(ctx, `select count(*) from coupons`).Scan(&coupons); err != nil {
		t.Fatal(err)
	}
	if coupons != 1 {
		t.Fatalf("coupon count = %d, want 1", coupons)
	}
}

func TestPostgresStoreMapsExpectedTransactionalConditions(t *testing.T) {
	for _, tt := range []struct {
		name string
		call func(context.Context, *PostgresStore) error
		want error
	}{
		{
			name: "missing coupon principal",
			call: func(ctx context.Context, store *PostgresStore) error {
				identity := postgresStoreTestIdentity(issueCouponOperation, "missing-principal")
				identity.PrincipalID = "missing-user"
				_, err := store.IssueCoupon(ctx, identity, 100, "service recovery")
				return err
			},
			want: ErrNotFound,
		},
		{
			name: "wrong order principal",
			call: func(ctx context.Context, store *PostgresStore) error {
				identity := postgresStoreTestIdentity(createReturnOperation, "wrong-principal")
				identity.PrincipalID = "user_999"
				_, err := store.CreateReturn(ctx, identity, "AG-1042", "damaged", postgresStoreTestTime)
				return err
			},
			want: ErrForbidden,
		},
		{
			name: "missing replacement inventory",
			call: func(ctx context.Context, store *PostgresStore) error {
				_, err := store.CreateReplacement(ctx, postgresStoreTestIdentity(createReplacementOperation, "missing-inventory"), "AG-1042", "MISSING-SKU", "damaged", postgresStoreTestTime)
				return err
			},
			want: ErrNotFound,
		},
		{
			name: "expired return",
			call: func(ctx context.Context, store *PostgresStore) error {
				_, err := store.CreateReturn(ctx, postgresStoreTestIdentity(createReturnOperation, "expired-return"), "AG-1042", "damaged", time.Date(2026, 9, 1, 12, 0, 0, 1, time.UTC))
				return err
			},
			want: ErrIneligible,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			if err := tt.call(context.Background(), store); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}
