package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateCreatesCommerceSchema(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	adminPool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	schema := fmt.Sprintf("task_2_migrate_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	migrationURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := migrationURL.Query()
	query.Set("search_path", schema+",public")
	migrationURL.RawQuery = query.Encode()

	pool, err := Open(ctx, migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var currentSchema string
	if err := pool.QueryRow(ctx, `select current_schema()`).Scan(&currentSchema); err != nil {
		t.Fatal(err)
	}
	if currentSchema != schema {
		t.Fatalf("current schema = %q, want %q", currentSchema, schema)
	}

	rows, err := pool.Query(ctx, `
		select table_name
		from information_schema.tables
		where table_schema = $1 and table_type = 'BASE TABLE'
		order by table_name
	`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	wantTables := []string{
		"coupons", "idempotency_records", "inventory", "orders", "products", "refunds",
		"replacements", "returns", "shipments", "users",
	}
	if !slices.Equal(tables, wantTables) {
		t.Fatalf("tables = %v, want %v", tables, wantTables)
	}

	requireConstraintSet(t, pool, schema, "c", "check constraints", []string{
		"coupons|coupons_amount_cents_check|CHECK ((amount_cents > 0))",
		"inventory|inventory_available_check|CHECK ((available >= 0))",
		"inventory|inventory_reserved_check|CHECK ((reserved >= 0))",
		"orders|orders_check|CHECK ((refunded_amount_cents <= paid_amount_cents))",
		"orders|orders_paid_amount_cents_check|CHECK ((paid_amount_cents >= 0))",
		"orders|orders_refunded_amount_cents_check|CHECK ((refunded_amount_cents >= 0))",
		"orders|orders_status_check|CHECK ((status = ANY (ARRAY['paid'::text, 'shipped'::text, 'delivered'::text, 'cancelled'::text])))",
		"refunds|refunds_amount_cents_check|CHECK ((amount_cents > 0))",
		"refunds|refunds_status_check|CHECK ((status = ANY (ARRAY['created'::text, 'settled'::text, 'failed'::text])))",
		"replacements|replacements_status_check|CHECK ((status = ANY (ARRAY['created'::text, 'shipped'::text, 'cancelled'::text])))",
		"returns|returns_status_check|CHECK ((status = ANY (ARRAY['created'::text, 'received'::text, 'closed'::text])))",
	})

	requireConstraintSet(t, pool, schema, "f", "foreign keys", []string{
		"coupons|coupons_user_id_fkey|FOREIGN KEY (user_id) REFERENCES users(id)",
		"inventory|inventory_sku_fkey|FOREIGN KEY (sku) REFERENCES products(sku)",
		"orders|orders_sku_fkey|FOREIGN KEY (sku) REFERENCES products(sku)",
		"orders|orders_user_id_fkey|FOREIGN KEY (user_id) REFERENCES users(id)",
		"refunds|refunds_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
		"replacements|replacements_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
		"replacements|replacements_sku_fkey|FOREIGN KEY (sku) REFERENCES products(sku)",
		"returns|returns_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
		"shipments|shipments_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
	})

	requireUniqueIndexSet(t, pool, schema, []string{
		"coupons|coupons_pkey|id",
		"idempotency_records|idempotency_records_pkey|operation,idempotency_key",
		"inventory|inventory_pkey|sku",
		"orders|orders_pkey|id",
		"products|products_pkey|sku",
		"refunds|refunds_pkey|id",
		"replacements|replacements_pkey|id",
		"returns|returns_pkey|id",
		"shipments|shipments_order_id_key|order_id",
		"shipments|shipments_pkey|id",
		"users|users_pkey|id",
	})

	requireColumnNullability(t, pool, schema, "idempotency_records", []string{
		"created_at|NO",
		"idempotency_key|NO",
		"operation|NO",
		"principal_id|NO",
		"request_fingerprint|NO",
		"result_id|NO",
		"result_type|NO",
	})
}

func TestMigrateUpgradesLegacyIdempotencyRecordsIdempotently(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	adminPool, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	schema := fmt.Sprintf("legacy_idempotency_%d", time.Now().UnixNano())
	if _, err := adminPool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := adminPool.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})
	if _, err := adminPool.Exec(ctx, `
		create table `+schema+`.idempotency_records (
			operation text not null,
			idempotency_key text not null,
			result_type text not null,
			result_id text not null,
			created_at timestamptz not null default now(),
			primary key (operation, idempotency_key)
		);
		insert into `+schema+`.idempotency_records (operation, idempotency_key, result_type, result_id)
		values ('create_return', 'legacy-key', 'return', 'legacy-id');
	`); err != nil {
		t.Fatal(err)
	}

	migrationURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := migrationURL.Query()
	query.Set("search_path", schema+",public")
	migrationURL.RawQuery = query.Encode()
	pool, err := Open(ctx, migrationURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	requireColumnNullability(t, pool, schema, "idempotency_records", []string{
		"created_at|NO",
		"idempotency_key|NO",
		"operation|NO",
		"principal_id|NO",
		"request_fingerprint|NO",
		"result_id|NO",
		"result_type|NO",
	})

	var principalID, fingerprint string
	if err := pool.QueryRow(ctx, `
		select principal_id, request_fingerprint
		from idempotency_records
		where operation = 'create_return' and idempotency_key = 'legacy-key'
	`).Scan(&principalID, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if principalID != "__legacy_unbound__" || fingerprint != strings.Repeat("0", 64) {
		t.Fatalf("legacy binding = principal %q fingerprint %q", principalID, fingerprint)
	}

	result, replayed, err := commerce.NewPostgresStore(pool).ReplayWrite(ctx, commerce.IdempotencyIdentity{
		Operation:          "create_return",
		Key:                "legacy-key",
		PrincipalID:        "user_018",
		RequestFingerprint: strings.Repeat("a", 64),
	})
	if !errors.Is(err, commerce.ErrIdempotencyConflict) || replayed || result != (commerce.WriteResult{}) {
		t.Fatalf("legacy replay = %#v, replayed=%t, err=%v; want empty conflict", result, replayed, err)
	}
}

func requireConstraintSet(t *testing.T, pool *pgxpool.Pool, schema, constraintType, name string, want []string) {
	t.Helper()

	rows, err := pool.Query(context.Background(), `
		select relation.relname || '|' || schema_constraint.conname || '|' || pg_get_constraintdef(schema_constraint.oid)
		from pg_constraint schema_constraint
		join pg_class relation on relation.oid = schema_constraint.conrelid
		join pg_namespace namespace on namespace.oid = relation.relnamespace
		where namespace.nspname = $1 and schema_constraint.contype = $2
		order by relation.relname, schema_constraint.conname
	`, schema, constraintType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var constraint string
		if err := rows.Scan(&constraint); err != nil {
			t.Fatal(err)
		}
		got = append(got, constraint)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s =\n%s\nwant:\n%s", name, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func requireUniqueIndexSet(t *testing.T, pool *pgxpool.Pool, schema string, want []string) {
	t.Helper()

	rows, err := pool.Query(context.Background(), `
		select relation.relname || '|' || index_relation.relname || '|' || string_agg(attribute.attname::text, ',' order by keys.ordinality)
		from pg_index index
		join pg_class relation on relation.oid = index.indrelid
		join pg_namespace namespace on namespace.oid = relation.relnamespace
		join pg_class index_relation on index_relation.oid = index.indexrelid
		join unnest(index.indkey::smallint[]) with ordinality as keys(attnum, ordinality) on true
		join pg_attribute attribute on attribute.attrelid = index.indrelid and attribute.attnum = keys.attnum
		where namespace.nspname = $1 and index.indisunique
		group by relation.relname, index_relation.relname
		order by relation.relname, index_relation.relname
	`, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var index string
		if err := rows.Scan(&index); err != nil {
			t.Fatal(err)
		}
		got = append(got, index)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unique indexes =\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func requireColumnNullability(t *testing.T, pool *pgxpool.Pool, schema, table string, want []string) {
	t.Helper()

	rows, err := pool.Query(context.Background(), `
		select column_name || '|' || is_nullable
		from information_schema.columns
		where table_schema = $1 and table_name = $2
		order by column_name
	`, schema, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		got = append(got, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s columns =\n%s\nwant:\n%s", table, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
