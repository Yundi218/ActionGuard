# Task 2 Report: Commerce Domain and PostgreSQL Store

## RED Evidence

Command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/database -run TestMigrateCreatesOrders -v
```

Relevant output:

```text
internal/database/migrate_test.go:15:15: undefined: Open
internal/database/migrate_test.go:21:12: undefined: Migrate
FAIL github.com/Yundi218/ActionGuard/internal/database [build failed]
```

Command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/commerce -run PostgresStore -v
```

Relevant output:

```text
github.com/Yundi218/ActionGuard/internal/database: no non-test Go files in .../internal/database
FAIL github.com/Yundi218/ActionGuard/internal/commerce [build failed]
```

This was the expected pre-implementation failure: the new store tests depended on the absent database production package.

Command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/commerce -run TestPostgresStoreWritesPrioritizeEmptyIdempotencyKey -v
```

Relevant output:

```text
--- FAIL: TestPostgresStoreWritesPrioritizeEmptyIdempotencyKey
    .../refund: err = invalid amount
    .../coupon: err = invalid amount
```

## GREEN Evidence

Command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/database -run TestMigrateCreatesOrders -v
```

Relevant output:

```text
--- PASS: TestMigrateCreatesOrders
PASS
```

Command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/commerce -run PostgresStore -v
```

Relevant output:

```text
PASS
ok github.com/Yundi218/ActionGuard/internal/commerce
```

The suite includes `TestPostgresStoreCreateReturnSerializesConcurrentSameKey`, which starts two goroutines with the same operation/key and verifies one return row, identical resource IDs, and exactly one replayed result.

Final commands:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/database ./internal/commerce -v
GOPROXY=https://proxy.golang.org,direct go vet ./...
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./...
```

Relevant output:

```text
ok github.com/Yundi218/ActionGuard/internal/database
ok github.com/Yundi218/ActionGuard/internal/commerce
ok github.com/Yundi218/ActionGuard/internal/config
ok github.com/Yundi218/ActionGuard/internal/httpapi
```

`go vet ./...` exited with code 0. Docker was not invoked because the brief states Docker is unavailable and supplies the running local PostgreSQL 16 instance used by all integration tests.

## Files Changed

- `go.mod`, `go.sum`
- `migrations/001_commerce.sql`, `migrations/embed.go`
- `internal/database/pool.go`, `internal/database/migrate.go`, `internal/database/migrate_test.go`
- `internal/commerce/model.go`, `internal/commerce/errors.go`, `internal/commerce/store.go`
- `internal/commerce/postgres_store.go`, `internal/commerce/postgres_store_test.go`

## Self-Review

- Schema includes every requested table, foreign key, uniqueness rule, and check constraint.
- Each write rejects an empty idempotency key before beginning a transaction.
- The shared write helper acquires `pg_advisory_xact_lock(hashtextextended(operation || ':' || idempotency_key, 0))` before querying `idempotency_records`.
- Replacement and refund operations lock mutable inventory/order rows with `FOR UPDATE` before changing balances.
- No Service business rules or Task 3 files were added.

## Concerns

- The requested `github.com/jackc/pgx/v5@v5.10.0` currently declares `go 1.25.0`; `go mod tidy` therefore updated the project Go directive from `1.24` to `1.25.0`.
- The configured `https://goproxy.bytedance.com` proxy could not resolve in this environment. Dependency setup and verification used `GOPROXY=https://proxy.golang.org,direct` after the configured-proxy attempt failed with `lookup goproxy.bytedance.com: no such host`.

## Dependency Correction (Go 1.24)

The corrected brief pins `github.com/jackc/pgx/v5` to `v5.8.0`, which is compatible with the approved Go 1.24 project constraint.

Command:

```sh
GOPROXY=https://proxy.golang.org,direct go get github.com/jackc/pgx/v5@v5.8.0 && go mod edit -go=1.24 && GOPROXY=https://proxy.golang.org,direct go mod tidy
```

Relevant output:

```text
go: downgraded github.com/jackc/pgx/v5 v5.10.0 => v5.8.0
go 1.24.0
github.com/jackc/pgx/v5 v5.8.0
```

Verification command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable GOPROXY=https://proxy.golang.org,direct go test ./internal/database ./internal/commerce -v && GOPROXY=https://proxy.golang.org,direct go test ./... && GOPROXY=https://proxy.golang.org,direct go vet ./...
```

Relevant output:

```text
PASS
ok github.com/Yundi218/ActionGuard/internal/database
ok github.com/Yundi218/ActionGuard/internal/commerce
ok github.com/Yundi218/ActionGuard/internal/config
ok github.com/Yundi218/ActionGuard/internal/httpapi
```

All commands exited with code 0. The local PostgreSQL integration suite includes the concurrent same-key idempotency test.

## Review Follow-up: PostgreSQL Transaction Guarantees

### TDD Evidence

The reviewer-specific tests were added before production changes. The existing Task 2 store already acquired the same transaction-scoped advisory lock before checking idempotency and already rolled back failed transactions, so the new lock, replay-fidelity, and rollback tests passed on their first behavioral run. No production-code change was meaningful or required; the review findings were gaps in regression coverage.

The initial isolated-migration test run was RED because its new catalog assertion compared PostgreSQL `name[]` to `text[]`:

```text
ERROR: operator does not exist: name[] = text[] (SQLSTATE 42883)
```

The test-only assertion was corrected to aggregate attribute names as `text`, then rerun GREEN.

### Added Coverage

- `TestPostgresStoreCreateReturnWaitsForSameIdempotencyLock` holds the exact `pg_advisory_xact_lock(hashtextextended(operation || ':' || key, 0))` from an acquired, separate connection. It observes the matching ungranted advisory lock in `pg_locks`, verifies the write has not completed, commits the holder transaction, then verifies the write completes.
- `TestPostgresStoreIssueRefundRollsBackAfterOverRefund` causes the order check constraint to fail after refund insertion and asserts no refund, no idempotency record, and an unchanged `refunded_amount_cents` value.
- `TestPostgresStoreCreateReturnIsIdempotent` now asserts that clearing only `Replayed` makes the replayed `WriteResult` exactly equal to the original, covering resource type, resource ID, and status.
- `TestMigrateCreatesCommerceSchema` creates a unique schema, sets its pool connection `search_path` to `<schema>,public`, runs the embedded migration there, verifies all ten commerce tables and representative check constraints, foreign keys, and unique indexes, and drops the schema with `CASCADE` during cleanup.

### Exact Test Evidence

Focused regression command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable go test ./internal/database -run TestMigrateCreatesCommerceSchema -v
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable go test ./internal/commerce -run 'TestPostgresStoreCreateReturnIsIdempotent|TestPostgresStoreCreateReturnWaitsForSameIdempotencyLock|TestPostgresStoreIssueRefundRollsBackAfterOverRefund' -v
```

Relevant output:

```text
--- PASS: TestMigrateCreatesCommerceSchema
PASS
ok   github.com/Yundi218/ActionGuard/internal/database
--- PASS: TestPostgresStoreCreateReturnIsIdempotent
--- PASS: TestPostgresStoreCreateReturnWaitsForSameIdempotencyLock
--- PASS: TestPostgresStoreIssueRefundRollsBackAfterOverRefund
PASS
ok   github.com/Yundi218/ActionGuard/internal/commerce
```

Required integration and repository verification command:

```sh
TEST_DATABASE_URL=postgres://bytedance@localhost:5432/actionguard?sslmode=disable go test ./internal/database ./internal/commerce -v
go test ./...
go vet ./...
```

Relevant output:

```text
ok   github.com/Yundi218/ActionGuard/internal/database
ok   github.com/Yundi218/ActionGuard/internal/commerce
?    github.com/Yundi218/ActionGuard/cmd/api [no test files]
ok   github.com/Yundi218/ActionGuard/internal/config
ok   github.com/Yundi218/ActionGuard/internal/httpapi
?    github.com/Yundi218/ActionGuard/migrations [no test files]
```

`go vet ./...` exited with code 0. `go.mod` remains `go 1.24.0` and pins `github.com/jackc/pgx/v5 v5.8.0`.

### Files Changed

- `internal/commerce/postgres_store_test.go`
- `internal/database/migrate_test.go`
- `.superpowers/sdd/task-2-report.md`

### Review Note

The existing deferred rollback continues to discard a rollback error after an earlier transaction error, as requested. No transaction-code complexity was added solely to change that behavior.

## Review Follow-up: Complete Commerce Schema Coverage

### Coverage Gap Demonstration

Before changing the migration test, the catalog assertions covered only two CHECK constraints, two foreign keys, and two unique indexes. The migration itself declares 11 CHECK constraints, 9 `REFERENCES` relationships, and 11 primary-key or unique rules. This showed that the passing representative test did not provide the requested full-schema contract coverage.

Command:

```sh
printf 'Existing migration-test catalog assertions:\n'; rg -n 'require(CheckConstraint|ForeignKey|UniqueIndex)\(' internal/database/migrate_test.go; printf '\nSchema declarations:\n'; printf 'CHECK: '; rg -io 'check \(' migrations/001_commerce.sql | wc -l; printf 'FOREIGN KEY / REFERENCES: '; rg -io '\breferences\s+[a-z_]+' migrations/001_commerce.sql | wc -l; printf 'PRIMARY KEY or UNIQUE: '; rg -io '\bprimary key\b|\bunique\b' migrations/001_commerce.sql | wc -l
```

Relevant output:

```text
Existing migration-test catalog assertions:
94: requireCheckConstraint(t, pool, schema+".orders", "refunded_amount_cents <= paid_amount_cents")
95: requireCheckConstraint(t, pool, schema+".inventory", "available >= 0")
96: requireForeignKey(t, pool, schema+".inventory", schema+".products")
97: requireForeignKey(t, pool, schema+".shipments", schema+".orders")
98: requireUniqueIndex(t, pool, schema+".shipments", "order_id")
99: requireUniqueIndex(t, pool, schema+".idempotency_records", "operation", "idempotency_key")

Schema declarations:
CHECK: 11
FOREIGN KEY / REFERENCES: 9
PRIMARY KEY or UNIQUE: 11
```

### Implementation

`TestMigrateCreatesCommerceSchema` retains its isolated schema and `search_path` setup, asserts all ten tables, and now compares schema-scoped PostgreSQL catalog sets exactly:

- all 11 named CHECK constraints with canonical definitions;
- all 9 named foreign keys with source columns and referenced table/column definitions; and
- all 11 unique indexes, including every primary-key index and `shipments.order_id` unique index.

No production schema or business-service code changed. The active migration is `migrations/001_commerce.sql`; this worktree does not contain the brief's older `migrations/000001_init.up.sql` filename.

### Test Evidence

The first uncached expanded-test run exposed a test-query syntax issue, which was corrected by renaming the reserved SQL alias `constraint` to `schema_constraint`:

```sh
TEST_DATABASE_URL='postgres://bytedance@localhost:5432/actionguard?sslmode=disable' go test ./internal/database -run TestMigrateCreatesCommerceSchema -v -count=1
```

Initial relevant output:

```text
migrate_test.go:95: ERROR: syntax error at or near "constraint" (SQLSTATE 42601)
FAIL
```

Final focused result:

```text
=== RUN   TestMigrateCreatesCommerceSchema
--- PASS: TestMigrateCreatesCommerceSchema
PASS
ok github.com/Yundi218/ActionGuard/internal/database
```

Full verification commands:

```sh
TEST_DATABASE_URL='postgres://bytedance@localhost:5432/actionguard?sslmode=disable' go test ./...
TEST_DATABASE_URL='postgres://bytedance@localhost:5432/actionguard?sslmode=disable' go vet ./...
```

Relevant output:

```text
ok github.com/Yundi218/ActionGuard/internal/commerce
ok github.com/Yundi218/ActionGuard/internal/config
ok github.com/Yundi218/ActionGuard/internal/database
ok github.com/Yundi218/ActionGuard/internal/httpapi
go vet ./... exited with code 0
```

### Files Changed

- `internal/database/migrate_test.go`
- `.superpowers/sdd/task-2-report.md`
