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
	"testing/fstest"
	"time"

	"github.com/Yundi218/ActionGuard/internal/commerce"
	"github.com/Yundi218/ActionGuard/migrations"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationFileNamesReturnsEveryNumberedSQLFileInLexicalOrder(t *testing.T) {
	files := fstest.MapFS{
		"010_last.sql":        {Data: []byte("select 10")},
		"002_second.sql":      {Data: []byte("select 2")},
		"001_first.sql":       {Data: []byte("select 1")},
		"README.md":           {Data: []byte("not a migration")},
		"003_not_sql.txt":     {Data: []byte("not a migration")},
		"nested/004_skip.sql": {Data: []byte("not a top-level migration")},
	}

	got, err := migrationFileNames(files)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"001_first.sql", "002_second.sql", "010_last.sql"}
	if !slices.Equal(got, want) {
		t.Fatalf("migration files = %v, want %v", got, want)
	}
}

func TestEmbeddedPolicyPlanningMigrationDeclaresVectorExtension(t *testing.T) {
	migration, err := migrations.FS.ReadFile("002_policy_planning.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migration), "create extension if not exists vector;") {
		t.Fatal("migration 002 must declare create extension if not exists vector")
	}
}

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
		"coupons", "idempotency_records", "inventory", "messages", "orders", "plans",
		"policy_chunks", "policy_documents", "products", "refunds", "replacements", "returns",
		"runs", "sessions", "shipments", "users",
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
		"runs|runs_status_check|CHECK ((status = ANY (ARRAY['planning'::text, 'needs_input'::text, 'ready'::text, 'running'::text, 'waiting_runtime'::text, 'succeeded'::text, 'failed'::text])))",
	})

	requireConstraintSet(t, pool, schema, "f", "foreign keys", []string{
		"coupons|coupons_user_id_fkey|FOREIGN KEY (user_id) REFERENCES users(id)",
		"inventory|inventory_sku_fkey|FOREIGN KEY (sku) REFERENCES products(sku)",
		"messages|messages_run_id_fkey|FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE",
		"messages|messages_session_id_fkey|FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE",
		"orders|orders_sku_fkey|FOREIGN KEY (sku) REFERENCES products(sku)",
		"orders|orders_user_id_fkey|FOREIGN KEY (user_id) REFERENCES users(id)",
		"plans|plans_run_id_fkey|FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE",
		"policy_chunks|policy_chunks_policy_id_version_fkey|FOREIGN KEY (policy_id, version) REFERENCES policy_documents(policy_id, version) ON DELETE CASCADE",
		"refunds|refunds_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
		"replacements|replacements_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
		"replacements|replacements_sku_fkey|FOREIGN KEY (sku) REFERENCES products(sku)",
		"returns|returns_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
		"runs|runs_session_id_fkey|FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE",
		"sessions|sessions_user_id_fkey|FOREIGN KEY (user_id) REFERENCES users(id)",
		"shipments|shipments_order_id_fkey|FOREIGN KEY (order_id) REFERENCES orders(id)",
	})

	requireUniqueIndexSet(t, pool, schema, []string{
		"coupons|coupons_pkey|id",
		"idempotency_records|idempotency_records_pkey|operation,idempotency_key",
		"inventory|inventory_pkey|sku",
		"messages|messages_pkey|id",
		"orders|orders_pkey|id",
		"plans|plans_pkey|run_id,plan_version",
		"policy_chunks|policy_chunks_pkey|id",
		"policy_documents|policy_documents_pkey|policy_id,version",
		"products|products_pkey|sku",
		"refunds|refunds_pkey|id",
		"replacements|replacements_pkey|id",
		"returns|returns_pkey|id",
		"runs|runs_pkey|id",
		"sessions|sessions_pkey|id",
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

func TestMigrateCreatesPolicyPlanningSchema(t *testing.T) {
	ctx, pool, schema := newMigrationTestPool(t, "policy_planning")

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	var vectorInstalled bool
	if err := pool.QueryRow(ctx, `select exists (select 1 from pg_extension where extname = 'vector')`).Scan(&vectorInstalled); err != nil {
		t.Fatal(err)
	}
	if !vectorInstalled {
		t.Fatal("vector extension is not installed")
	}

	rows, err := pool.Query(ctx, `
		select table_name
		from information_schema.tables
		where table_schema = $1
		  and table_name in ('policy_documents', 'policy_chunks', 'sessions', 'messages', 'runs', 'plans')
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
	wantTables := []string{"messages", "plans", "policy_chunks", "policy_documents", "runs", "sessions"}
	if !slices.Equal(tables, wantTables) {
		t.Fatalf("phase 2 tables = %v, want %v", tables, wantTables)
	}

	var nullable, defaultValue string
	if err := pool.QueryRow(ctx, `
		select is_nullable, column_default
		from information_schema.columns
		where table_schema = $1 and table_name = 'products' and column_name = 'category'
	`, schema).Scan(&nullable, &defaultValue); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" || defaultValue != "'general'::text" {
		t.Fatalf("products.category = nullable %q default %q", nullable, defaultValue)
	}

	var embeddingType string
	if err := pool.QueryRow(ctx, `
		select pg_catalog.format_type(attribute.atttypid, attribute.atttypmod)
		from pg_catalog.pg_attribute attribute
		join pg_catalog.pg_class relation on relation.oid = attribute.attrelid
		join pg_catalog.pg_namespace namespace on namespace.oid = relation.relnamespace
		where namespace.nspname = $1 and relation.relname = 'policy_chunks' and attribute.attname = 'embedding'
	`, schema).Scan(&embeddingType); err != nil {
		t.Fatal(err)
	}
	if embeddingType != "vector(1536)" {
		t.Fatalf("policy_chunks.embedding type = %q", embeddingType)
	}

	requireColumnTypes(t, pool, schema, []string{
		"plans|evidence_json|jsonb",
		"plans|plan_json|jsonb",
		"plans|verification_json|jsonb",
		"runs|result_json|jsonb",
	})

	if _, err := pool.Exec(ctx, `
		insert into policy_documents (
			policy_id, version, source_name, effective_from, effective_to, region,
			product_category, risk_level, content_sha256
		) values ('returns', 'v1', 'returns-v1.md', now(), now() + interval '1 day', 'CN', 'general', 'write', repeat('a', 64))
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into policy_documents (
			policy_id, version, source_name, effective_from, effective_to, region,
			product_category, risk_level, content_sha256
		) values ('returns', 'v1', 'duplicate.md', now(), now() + interval '1 day', 'CN', 'general', 'write', repeat('b', 64))
	`); testPostgresErrorCode(err) != "23505" {
		t.Fatalf("duplicate policy error = %v, want unique violation", err)
	}

	vectorLiteral := "[" + strings.TrimSuffix(strings.Repeat("0,", 1536), ",") + "]"
	if _, err := pool.Exec(ctx, `
		insert into policy_chunks (
			id, policy_id, version, section, content, start_offset, end_offset, embedding
		) values ('chunk-1', 'returns', 'v1', 'Eligibility', 'Synthetic policy text.', 0, 22, $1)
	`, vectorLiteral); err != nil {
		t.Fatalf("store vector(1536): %v", err)
	}
}

func TestMigrateUpgradesSchema001AndRemainsIdempotent(t *testing.T) {
	ctx, pool, schema := newMigrationTestPool(t, "upgrade_001")

	commerceMigration, err := migrations.FS.ReadFile("001_commerce.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(commerceMigration)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into products (sku, name) values ('LEGACY-SKU', 'Legacy Product')`); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := pool.QueryRow(ctx, `
		select count(*) from information_schema.tables
		where table_schema = $1 and table_name = 'policy_documents'
	`, schema).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("policy_documents existed before migration 002")
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	var category string
	if err := pool.QueryRow(ctx, `select category from products where sku = 'LEGACY-SKU'`).Scan(&category); err != nil {
		t.Fatal(err)
	}
	if category != "general" {
		t.Fatalf("legacy product category = %q, want general", category)
	}

	var after int
	if err := pool.QueryRow(ctx, `
		select count(*) from information_schema.tables
		where table_schema = $1 and table_name = 'policy_documents'
	`, schema).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("policy_documents count = %d, want 1", after)
	}
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

func newMigrationTestPool(t *testing.T, prefix string) (context.Context, *pgxpool.Pool, string) {
	t.Helper()

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

	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
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
	return ctx, pool, schema
}

func requireColumnTypes(t *testing.T, pool *pgxpool.Pool, schema string, want []string) {
	t.Helper()

	rows, err := pool.Query(context.Background(), `
		select table_name || '|' || column_name || '|' || data_type
		from information_schema.columns
		where table_schema = $1
		  and ((table_name = 'plans' and column_name in ('plan_json', 'evidence_json', 'verification_json'))
		    or (table_name = 'runs' and column_name = 'result_json'))
		order by table_name, column_name
	`, schema)
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
		t.Fatalf("json column types =\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func testPostgresErrorCode(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return ""
}
