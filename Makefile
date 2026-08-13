.PHONY: test vet db-up db-down api mcp

test:
	go test ./...

vet:
	go vet ./...

db-up:
	docker compose -f deploy/docker-compose.yml up -d postgres

db-down:
	docker compose -f deploy/docker-compose.yml down -v

api:
	go run ./cmd/api

mcp:
	go run ./cmd/commerce-mcp
