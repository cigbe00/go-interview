# Maoni Senior Backend Engineer — Practical Exercise

## Why this exercise exists

Maoni is evaluating your ability to inherit an unfamiliar Go backend, understand it with minimal supervision, diagnose production-style failures, make focused fixes, integrate external providers safely, and leave the codebase more reliable than you found it.

This repository is intentionally small and uses **mock application data only**. It does **not** connect to Maoni production systems or databases. Redis runs locally in Docker. You do not need—and should not request—Maoni Redis, Google, Paystack, AWS, database, or other production credentials.

The application mirrors patterns relevant to our backend: Go, REST APIs, handler/service/repository separation, Redis caching, authentication integration, payment/subscription integration, background-safe/idempotent thinking, and production debugging.

> Some behavior is deliberately incorrect. The repository should compile and its starter tests should pass; the bugs are intentionally under-tested. Part of the assessment is writing tests that reproduce the reported symptoms before or while fixing them.

## Expected time

Please aim for approximately **3–4 focused hours**. We care more about correctness, reasoning, tests, and maintainability than the amount of code written. If you run out of time, document what remains and how you would approach it.

## Prerequisites

- Go **1.23+**
- Git
- Docker Desktop or Docker Engine with `docker compose`
- `curl` (optional; `requests.http` is also provided)

## Quick start

### 1. Clone and create your branch

```bash
git clone <repository-url>
cd maoni-backend-takehome
git checkout -b candidate/<your-name>
```

### 2. Create local environment configuration

```bash
cp .env.example .env
```

The supplied values are local/placeholder values. **Do not add real secrets to Git.**

### 3. Start Redis locally

```bash
docker compose up -d redis
```

Confirm it is healthy:

```bash
docker compose exec redis redis-cli ping
```

Expected output:

```text
PONG
```

Redis is local only. The default address is `localhost:6379`, with no password. No Maoni Redis credentials are required.

If port `6379` is already in use, you may choose another host port, for example:

```bash
REDIS_PORT=6380 docker compose up -d redis
```

and update `REDIS_ADDR` in `.env` to `localhost:6380`.

### 4. Download Go dependencies

```bash
go mod download
```

### 5. Run the starter tests

```bash
go test ./...
```

The provided starter tests are expected to pass. They do **not** cover all known defects. Add tests for the defects you fix and for the integrations you implement.

### 6. Run the API

```bash
go run ./cmd/api
```

The application loads `.env` automatically when present and starts on:

```text
http://localhost:8080
```

### 7. Health check

```bash
curl http://localhost:8080/health
```

Expected response:

```json
{"status":"ok"}
```

The health response also reports whether the application was able to connect to Redis.

---

# Tasks to be completed

## Task 1 — Debug the existing REST API

Several user-visible behaviors are incorrect. Investigate the code and fix the **root causes**, not just the returned JSON.

Known symptoms include:

1. A seeded business can unexpectedly return `404` when requested by its documented ID.
2. Creating a review can return `201 Created`, but the business's review count and/or average rating does not reliably reflect the new review.
3. A business's average rating can lose precision.
4. Review pagination can skip records or return an unexpected page.
5. After data changes, a subsequent business request may continue returning stale information for a period of time.

There may be other issues you notice. Fix anything materially incorrect that you can justify, and explain it in the Pull Request.

### What we are evaluating

- How you trace a symptom through handler -> service -> data/cache/provider layers
- Whether you identify the actual root cause(s)
- Whether your fixes preserve data consistency
- Whether you add regression tests
- Whether your changes are focused rather than a rewrite

---

## Task 2 — Redis cache behavior

Redis support and local Docker setup are already provided. You are **not** expected to build Redis infrastructure from scratch.

Review how business responses are cached and make the behavior production-sensible.

At minimum:

- Correct stale-cache behavior when a review changes business-derived data.
- Preserve sensible cache keying and TTL behavior.
- Avoid making a transient cache failure corrupt application data.
- Add tests around the cache-dependent behavior you fix. A fake cache is acceptable for service tests; an optional local Redis integration test is welcome.

Do not use any remote Redis instance or Maoni credentials.

---

## Task 3 — Implement Google sign-in verification

Complete the Google ID-token verification path behind the existing `auth.TokenVerifier` interface.

For this exercise, the starter includes an injectable Google token-info URL so your implementation can be tested against `httptest.Server` without Google credentials or live network calls. In your PR, briefly note whether you would use the same mechanism in production or switch to Google's server-side ID-token/JWK validation library.

Requirements:

- Do not hard-code credentials or client IDs.
- Read the configured audience/client ID from environment-backed configuration.
- Respect request context and use an HTTP client with a timeout.
- Reject empty or invalid tokens.
- Validate that the returned token audience matches the configured Google client ID.
- Validate issuer and expiration information.
- Require a stable Google subject (`sub`) and a usable, verified email identity.
- Normalize the provider response into the existing `auth.Identity` model.
- Return sensible errors for invalid tokens versus provider/network failures.
- Add tests using a local/mock HTTP server. Tests must not require real Google credentials or outbound internet access.

Important: do not use email as the provider's stable account identifier; preserve Google's `sub` identifier.

---

## Task 4 — Implement Paystack transaction/subscription flow

Complete the Paystack client and subscription flow behind the existing payment interfaces. You do **not** need a Maoni Paystack key and should not call live Paystack from automated tests.

Requirements:

- Implement transaction initialization using the configured base URL and secret key.
- Send provider authentication correctly and use JSON request/response handling.
- Support the supplied plan code and preserve correlation to the Maoni user initiating the subscription.
- Use context-aware HTTP requests and a reasonable timeout.
- Handle non-2xx responses, provider-declared failures, malformed JSON, and missing required response data safely.
- Verify Paystack webhook origin using the `x-paystack-signature` value and the raw request body.
- Parse the relevant webhook payload into the existing normalized event model.
- Make webhook processing idempotent so a duplicate delivery cannot apply the same subscription change twice.
- Ensure a webhook updates the correct Maoni subscription/user rather than relying on an unsafe identity assumption.
- Add tests with `httptest.Server` or equivalent. No live Paystack calls are required.

You may implement only the webhook event types necessary to demonstrate a sound subscription/payment flow; document what additional events you would support in production.

---

## Task 5 — Improve API robustness where appropriate

We will also look for pragmatic senior-level improvements you notice while working, such as:

- Correct request validation and HTTP status codes
- Safe request-body handling
- Context propagation and timeouts
- Clear error boundaries between provider, service, and API layers
- Useful logging/observability hooks
- Safe concurrency and idempotency
- Appropriate tests
- Avoiding unnecessary abstractions or broad rewrites

Please do **not** replace Echo, change the exercise into another framework, or rewrite the entire application. We want to evaluate how you improve an inherited codebase.

---

# Useful endpoints

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

Example requests are available in [`requests.http`](requests.http).

## Useful seeded data

```text
Business ID: biz_1
Business ID: biz_2
```

The mock repository is reset each time the API process restarts. Redis may retain cached values until its TTL expires; use `docker compose exec redis redis-cli FLUSHDB` if you intentionally need a clean cache during development.

---

# Submission instructions

1. Work on your own branch.
2. Commit changes with meaningful commit messages.
3. Push your branch and open a Pull Request to the provided repository.
4. Do not commit `.env`, credentials, generated binaries, or unrelated files.
5. Keep the application runnable locally using the documented setup.

Your Pull Request description must include:

- The bugs/root causes you identified
- What you changed and why
- Tests you added
- Trade-offs or assumptions you made
- Any known remaining issues
- What you would improve next with more time
- How you would monitor the review, Google-auth, Redis, and Paystack flows in production

## Evaluation priorities

We prioritize:

1. Debugging/root-cause reasoning
2. Correctness and data consistency
3. Go/API code quality
4. Redis/cache correctness
5. Paystack integration and webhook safety
6. Google authentication implementation
7. Tests and regression prevention
8. Communication, documentation, and scope judgment

A small, correct, well-tested change will score higher than a large rewrite.

## Security and confidentiality

This repository contains no Maoni production data or secrets. Do not add any real production credential to the project. If you choose to use your own provider test credentials for manual exploration, keep them only in your untracked `.env` file...
