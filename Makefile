.PHONY: test vet db-up db-down api mcp fixtures policies test-integration test-e2e demo-up

test:
	go test -p 1 ./...

vet:
	go vet ./...

db-up:
	docker compose -f deploy/docker-compose.yml up -d --wait postgres

db-down:
	docker compose -f deploy/docker-compose.yml down -v

api:
	go run ./cmd/api

mcp:
	go run ./cmd/commerce-mcp

fixtures:
	DATABASE_URL=$${DATABASE_URL} bash scripts/load-fixtures.sh

policies:
	go run ./cmd/policy-import

test-integration:
	@test -n "$${DATABASE_URL}" || (printf 'error: DATABASE_URL is required\n' >&2; exit 1)
	TEST_DATABASE_URL=$${DATABASE_URL} go test -p 1 ./... -count=1

test-e2e:
	@test -n "$${DATABASE_URL}" || (printf 'error: DATABASE_URL is required\n' >&2; exit 1)
	TEST_DATABASE_URL=$${DATABASE_URL} go test -p 1 ./internal/orchestrator -run '^TestPolicyConstrainedReplacementFlow$$' -count=1 -v

demo-up:
	docker compose -f deploy/docker-compose.yml up --build
