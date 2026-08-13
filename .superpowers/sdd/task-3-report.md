# Task 3 Report: Commerce Operation Invariants

## RED Evidence

Service tests were added before `internal/commerce/service.go` existed.

Command:

```text
gofmt -w internal/commerce/service_test.go internal/toolkit/contract_test.go
go test ./internal/commerce ./internal/toolkit -run 'Service|Eligibility|Metadata|Scope|CallContext|Envelope' -v
```

Result: failed as expected. `internal/commerce/service_test.go` reported `undefined: NewService` and `undefined: NewServiceWithClock`. The toolkit package could not initially build because the default Go build cache was outside the sandbox's writable paths.

Command:

```text
GOCACHE=/private/tmp/actionguard-go-build go test ./internal/toolkit -run 'Metadata|Scope|CallContext|Envelope' -v
```

Result: failed as expected. `internal/toolkit/contract_test.go` reported undefined `CallContext`, `Risk`, `Read`, `Write`, and `HighRiskWrite` symbols.

## GREEN Evidence

Command:

```text
gofmt -w internal/commerce/service.go internal/commerce/service_test.go internal/toolkit/contract.go internal/toolkit/contract_test.go
GOCACHE=/private/tmp/actionguard-go-build go test ./internal/commerce ./internal/toolkit -v
```

Result: passed. All service and toolkit tests passed. Existing PostgreSQL store tests were skipped because `TEST_DATABASE_URL` is not set.

Command:

```text
GOCACHE=/private/tmp/actionguard-go-build go test ./...
```

Result: passed with exit code 0 for `cmd/api`, `internal/commerce`, `internal/config`, `internal/database`, `internal/httpapi`, `internal/toolkit`, and `migrations`.

Command:

```text
GOCACHE=/private/tmp/actionguard-go-build go vet ./...
```

Result: passed with exit code 0 and no diagnostics.

Command:

```text
git diff --check
```

Result: passed with exit code 0 and no whitespace errors.

## Changed Files

- `internal/commerce/service.go`
- `internal/commerce/service_test.go`
- `internal/toolkit/contract.go`
- `internal/toolkit/contract_test.go`
- `.superpowers/sdd/task-3-report.md`

## Deviations

- Used `GOCACHE=/private/tmp/actionguard-go-build` for Go test and vet commands because the default build cache under `/Users/bytedance/Library/Caches/go-build` is not writable in this sandbox.
- Formatted the explicit Task 3 Go files because `gofmt` does not recursively accept directories.
- No Task 2 interface mismatch was found; no Task 2 files were changed.

## Review Fix Evidence (2026-08-13)

### RED Evidence

The JSON-rejection regression test was added before the trusted-context decoding guard.

Command:

```text
gofmt -w internal/commerce/service_test.go internal/toolkit/contract_test.go
GOCACHE=/private/tmp/actionguard-go-build go test ./internal/commerce ./internal/toolkit -run 'CallContextRejectsJSON|PropagatesErrorsFromReachedStoreMethods|CreateReplacementRejectsIneligibleOrderWithoutCheckingInventoryOrWriting|ForwardsContextToEveryReachedStoreMethod' -v
```

Result: failed as expected. `internal/toolkit/contract_test.go:73:21` reported `undefined: ErrUntrustedCallContextJSON`. The independently injected service error, ineligible replacement, and context-propagation coverage already passed against the existing service behavior.

### GREEN Evidence

Command:

```text
gofmt -w internal/commerce/service_test.go internal/toolkit/contract.go internal/toolkit/contract_test.go
GOCACHE=/private/tmp/actionguard-go-build go test ./internal/commerce ./internal/toolkit -run 'CallContextRejectsJSON|PropagatesErrorsFromReachedStoreMethods|CreateReplacementRejectsIneligibleOrderWithoutCheckingInventoryOrWriting|ForwardsContextToEveryReachedStoreMethod' -v
```

Result: passed. `TestCallContextRejectsJSON` proved `json.Unmarshal` returns `ErrUntrustedCallContextJSON` and leaves existing trusted metadata unchanged. The service tests independently propagated errors from `GetShipmentByOrder`, `CreateReturn`, replacement `GetInventory`, `CreateReplacement`, and `IssueRefund`; verified ineligible replacements skip inventory and writes; and verified the supplied context reaches every reached `Store` method.

Command:

```text
gofmt -w internal/commerce/service.go internal/commerce/service_test.go internal/toolkit/contract.go internal/toolkit/contract_test.go
GOCACHE=/private/tmp/actionguard-go-build go test ./internal/commerce ./internal/toolkit -v
```

Result: passed. PostgreSQL integration tests were skipped because `TEST_DATABASE_URL` is not set.

Command:

```text
GOCACHE=/private/tmp/actionguard-go-build go test ./...
```

Result: passed with exit code 0 for `cmd/api`, `internal/commerce`, `internal/config`, `internal/database`, `internal/httpapi`, `internal/toolkit`, and `migrations`.

Command:

```text
GOCACHE=/private/tmp/actionguard-go-build go vet ./...
```

Result: passed with exit code 0 and no diagnostics.

### Changed Files

- `internal/toolkit/contract.go`
- `internal/toolkit/contract_test.go`
- `internal/commerce/service_test.go`
- `.superpowers/sdd/task-3-report.md`
