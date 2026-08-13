# Maoni Backend API

A focused Go backend that demonstrates reliable review aggregation, cache consistency, Google identity verification, and a secure Paystack subscription flow. The implementation keeps the original handler → service → store/provider boundaries while fixing the deliberately seeded defects with small, testable changes.

## Engineering highlights

- Correct, concurrency-safe review writes and derived rating statistics
- Cache-aside reads with best-effort Redis invalidation after successful writes
- Context-aware Google and Paystack requests with bounded clients and response bodies
- Google claim validation for audience, issuer, expiry, stable subject, and verified email
- HMAC-SHA512 Paystack webhook verification using the unmodified request body
- Atomic webhook correlation and idempotency based on event ID, user metadata, transaction reference, and plan
- Strict JSON decoding, 1 MiB request limits, validated pagination, and server timeouts
- Provider tests use `httptest.Server`; no real credentials or outbound calls are required

## Architecture

```text
HTTP / Echo
    │
    ├── BusinessService ── MemoryStore
    │         └─────────── Redis business cache
    │
    ├── AuthService ────── Google TokenInfo verifier
    │         └─────────── MemoryStore
    │
    └── SubscriptionService ── Paystack client
              └─────────────── MemoryStore
```

The in-memory store is intentional for this exercise. Provider and cache boundaries are interfaces, so core service behavior is tested without infrastructure, while HTTP provider behavior is exercised against local mock servers.

## Root causes and fixes

| Symptom | Root cause | Resolution |
|---|---|---|
| `GET /businesses/biz_1` returned `404` | The store compared the route ID only with each business slug | Resolve the map key first and retain slug lookup as a convenience |
| A created review did not affect statistics | Reviews were written to `review`, but reads used `reviews` | Use the shared collection constant for reads and writes |
| Average ratings lost precision | Integer division occurred before conversion to `float64` | Convert operands before division |
| Page 1 skipped records | Offset was calculated as `page * limit` | Use the one-based offset `(page - 1) * limit` |
| Business data stayed stale after a review | The cache entry survived the authoritative write | Evict the affected business after the review commits |
| Google users could be merged by email | User storage treated email as the identity key | Key Google users by the provider's stable `sub` claim |
| Webhooks could update the wrong user | The service used the customer email as `userID` | Correlate signed metadata `user_id` with the pending reference and plan |
| Duplicate/invalid events could corrupt state | Event marking and subscription writes were separate and weakly correlated | Validate and apply the event atomically under the store lock |
| Chunked webhook bodies could be truncated | The handler allocated from `Content-Length` and called `Read` once | Use a bounded `io.ReadAll` over the raw body |

Cache failures never change the result of an authoritative store operation. A cache read error becomes a miss; a cache set or delete error is best effort. In a production service these failures would also emit metrics and structured logs.

## Run locally

### Prerequisites

- Go 1.23+
- Docker with Compose
- `curl` or an HTTP client that supports [`requests.http`](requests.http)

### Setup

```bash
cp .env.example .env
docker compose up -d redis
docker compose exec redis redis-cli ping
go mod download
go test ./...
go run ./cmd/api
```

The API listens on `http://localhost:8080`. A healthy local response is:

```bash
curl http://localhost:8080/health
```

```json
{"redis":true,"status":"ok"}
```

If Redis is unavailable at startup, the service logs the failure and continues with a no-op cache. The health endpoint reports `redis: false` while the API remains available.

If port `6379` is occupied:

```bash
REDIS_PORT=6380 docker compose up -d redis
```

Then set `REDIS_ADDR=localhost:6380` in `.env`.

## Configuration

Configuration is loaded from the environment; an untracked `.env` is loaded for local development.

| Variable | Default | Purpose |
|---|---:|---|
| `PORT` | `8080` | HTTP listen port |
| `REDIS_ADDR` | `localhost:6379` | Redis address |
| `REDIS_PASSWORD` | empty | Redis password |
| `REDIS_DB` | `0` | Redis database |
| `REDIS_BUSINESS_TTL_SECONDS` | `300` | Cached business TTL |
| `GOOGLE_CLIENT_ID` | empty | Expected Google ID-token audience |
| `GOOGLE_TOKENINFO_URL` | Google TokenInfo endpoint | Injectable verification endpoint |
| `PAYSTACK_SECRET_KEY` | empty | API authentication and webhook signing key |
| `PAYSTACK_BASE_URL` | `https://api.paystack.co` | Injectable Paystack API base URL |
| `PAYSTACK_HTTP_TIMEOUT_SECONDS` | `5` | External-provider request timeout |

Do not commit real credentials. Automated tests require none.

## API

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/health` | API and Redis reachability |
| `GET` | `/api/v1/businesses/:id` | Business with derived review statistics |
| `GET` | `/api/v1/businesses/:id/reviews?page=1&limit=10` | Reviews ordered newest first |
| `POST` | `/api/v1/businesses/:id/reviews` | Create a 1–5 star review |
| `POST` | `/api/v1/auth/google` | Verify a Google ID token and upsert its user |
| `POST` | `/api/v1/subscriptions/initialize` | Initialize a Paystack plan transaction |
| `POST` | `/api/v1/subscriptions/webhook` | Process a signed `charge.success` event |
| `GET` | `/api/v1/subscriptions/:userID` | Read subscription state |

Seeded businesses are `biz_1` and `biz_2`. See [`requests.http`](requests.http) for ready-to-run examples.

### Review example

```bash
curl -X POST http://localhost:8080/api/v1/businesses/biz_1/reviews \
  -H 'Content-Type: application/json' \
  -d '{"user_id":"user_99","rating":5,"body":"Great place"}'
```

### Paystack flow

1. Initialization creates an application reference and sends email, amount, plan, reference, and `metadata.user_id` to Paystack.
2. A pending subscription is stored only after Paystack returns all required fields.
3. The webhook handler reads the exact raw body and verifies `x-paystack-signature` with HMAC-SHA512.
4. A successful `charge.success` event is normalized to `active` only when its signed user metadata, transaction reference, and plan correlate with the pending subscription.
5. Applying the state transition and recording the event ID occur in one critical section, making retries idempotent.

## Verification

```bash
make test       # unit and provider-contract tests
make test-race  # concurrency verification
make vet        # static analysis
make check      # test + vet
```

Regression coverage includes business ID lookup, saved-review visibility, rating precision, pagination, cache invalidation, Google claim validation and provider failures, Paystack authentication/payload handling, webhook signatures, correlation, and duplicate delivery.

## Production considerations

This submission deliberately avoids a broad rewrite, but the next production steps are clear:

- Replace `MemoryStore` with a transactional database repository. Enforce unique provider-subject and payment-event constraints in the database so correctness holds across replicas.
- Use an outbox/inbox pattern for durable webhook ingestion and asynchronous processing. Return success only after durable receipt, then retry state transitions safely.
- Prefer Google's server-side ID-token/JWK validation library in production. The TokenInfo approach remains here because its injectable URL makes the exercise deterministic without network access.
- Support additional Paystack lifecycle events such as failed charges, cancellations, renewals, and plan changes through an explicit state machine.
- Authenticate application endpoints and derive `user_id` from the authenticated principal rather than accepting it from public request JSON.
- Add structured logs, traces, and metrics for request latency/error rate, cache hits and failures, provider latency/status, signature failures, webhook duplicates, and event processing lag. Alert on sustained provider errors, cache degradation, and webhook backlog.
- Add graceful shutdown and separate liveness/readiness probes. Redis should remain an optional dependency for liveness and an observable dependency for readiness according to deployment policy.

## Trade-offs

- Cache invalidation happens after the store write. A transient delete failure can leave data stale until the TTL, but never rolls back or misreports a successful review write. A versioned key or transactional outbox would close that window in a distributed deployment.
- Idempotency is process-local because persistence is in memory. The atomic contract is correct for this implementation; production durability belongs in a database unique constraint.
- Only `charge.success` is normalized because it is sufficient to demonstrate the subscription activation path safely. Unsupported event types are rejected explicitly instead of being guessed into state changes.
