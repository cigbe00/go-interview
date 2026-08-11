# Maoni Backend Engineering Exercise

A small, standalone Go REST API that mirrors some of the patterns and failure modes we care about at Maoni.

The service uses **in-memory mock data only**. No database, Redis instance, AWS account, Paystack credentials, or Google credentials are required to complete the exercise.

## Stack

- Go 1.23+
- Echo REST API
- In-memory repository layer
- External-provider interfaces for Google authentication and Paystack
- `httptest`-friendly design for provider integration tests

## Run locally

```bash
go mod download
go test ./...
go run ./cmd/api
```

The API starts on `http://localhost:8080` unless `PORT` is set.

## Useful endpoints

```text
GET  /health
GET  /api/v1/businesses/:id
GET  /api/v1/businesses/:id/reviews?page=1&limit=10
POST /api/v1/businesses/:id/reviews
POST /api/v1/auth/google
POST /api/v1/subscriptions/initialize
POST /api/v1/subscriptions/webhook
GET  /api/v1/subscriptions/:userID
```

See [TASK.md](TASK.md) for the assignment.
