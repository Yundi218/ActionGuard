package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

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

	requireCheckConstraint(t, pool, schema+".orders", "refunded_amount_cents <= paid_amount_cents")
	requireCheckConstraint(t, pool, schema+".inventory", "available >= 0")
	requireForeignKey(t, pool, schema+".inventory", schema+".products")
	requireForeignKey(t, pool, schema+".shipments", schema+".orders")
	requireUniqueIndex(t, pool, schema+".shipments", "order_id")
	requireUniqueIndex(t, pool, schema+".idempotency_records", "operation", "idempotency_key")
}

func requireCheckConstraint(t *testing.T, pool *pgxpool.Pool, relation, definition string) {
	t.Helper()

	var found bool
	err := pool.QueryRow(context.Background(), `
		select exists (
			select 1
			from pg_constraint
			where conrelid = $1::regclass
			  and contype = 'c'
			  and lower(pg_get_constraintdef(oid)) like '%' || lower($2) || '%'
		)
	`, relation, definition).Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("missing check constraint on %s containing %q", relation, definition)
	}
}

func requireForeignKey(t *testing.T, pool *pgxpool.Pool, relation, target string) {
	t.Helper()

	var found bool
	err := pool.QueryRow(context.Background(), `
		select exists (
			select 1
			from pg_constraint
			where conrelid = $1::regclass
			  and contype = 'f'
			  and confrelid = $2::regclass
		)
	`, relation, target).Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("missing foreign key from %s to %s", relation, target)
	}
}

func requireUniqueIndex(t *testing.T, pool *pgxpool.Pool, relation string, columns ...string) {
	t.Helper()

	var found bool
	err := pool.QueryRow(context.Background(), `
		select exists (
			select 1
			from pg_index index
			cross join lateral (
				select array_agg(attribute.attname::text order by keys.ordinality) as columns
				from unnest(index.indkey::smallint[]) with ordinality as keys(attnum, ordinality)
				join pg_attribute attribute
				  on attribute.attrelid = index.indrelid and attribute.attnum = keys.attnum
			) index_columns
			where index.indrelid = $1::regclass
			  and index.indisunique
			  and index_columns.columns = $2::text[]
		)
	`, relation, columns).Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("missing unique index on %s(%v)", relation, columns)
	}
}
