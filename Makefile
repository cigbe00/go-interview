.PHONY: setup redis-up redis-down redis-status run test test-race fmt vet check

setup:
	@test -f .env || cp .env.example .env
	@echo "Created .env (if missing). Start Redis with: make redis-up"

redis-up:
	docker compose up -d redis

redis-down:
	docker compose down

redis-status:
	docker compose ps
	docker compose exec redis redis-cli ping

run:
	go run ./cmd/api

test:
	go test ./...

test-race:
	go test -race ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

check: test vet
