.PHONY: test vet db-up db-down api

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
