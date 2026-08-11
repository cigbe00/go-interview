.PHONY: run test fmt
run:
	go run ./cmd/api

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal
