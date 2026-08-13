# Maoni backend take-home — submission notes

This file is the pull request description, kept in the branch so it travels with the code.

**Branch:** `candidate/timileyin-bamgbose`

```bash
cp .env.example .env
docker compose up -d redis          # or REDIS_PORT=6380 docker compose up -d redis
go test ./... -race                 # Redis integration tests skip when Redis is not reachable
go run ./cmd/api
```

---

## 1. Bugs and root causes

Each root cause below has a regression test that fails against the original code.

| # | Symptom | Root cause | Fix |
|---|---|---|---|
| 1 | Seeded business 404s on its documented ID | `MemoryStore.GetBusiness` matched `b.Slug` against the requested ID, so `biz_1` never matched (`lagos-bistro` would have) | Look up by primary identifier ([memory.go](internal/store/memory.go)) |
| 2 | `201 Created` but review count / average unchanged | `SaveReview` appended to `s.collections["review"]`; every reader used `ReviewsCollection` (`"reviews"`). The write went into a collection nothing read | Write to `ReviewsCollection` |
| 3 | Average rating loses precision | `return count, float64(total / count)` — integer division happened *before* the conversion, so 5+4+4 over 3 reviews reported `4`, not `4.333…` | `float64(total) / float64(count)` |
| 4 | Pagination skips records / returns the wrong page | Offset was `page * limit` while the service normalises `page` to 1-based, so page 1 started at offset `limit` and silently skipped the newest records. Separately, `sort.Slice` is not stable and the comparison used `CreatedAt` alone, so reviews written in the same instant had no total order — a record could be skipped on one page and repeated on the next | `(page-1) * limit`, plus review ID as a tiebreaker for a total order |
| 5 | Stale business data after a write | The cached business embeds `review_count` and `average_rating`, and `CreateReview` never invalidated it, so reads served the pre-write snapshot for the rest of the TTL (default 5 minutes) | `CreateReview` invalidates after the write commits |

### Further defects found while working

| Area | Problem | Fix |
|---|---|---|
| Webhook body | The handler sized a buffer from `Content-Length` and called `Body.Read` **once**. A chunked request reports `-1` (buffer of zero), and a single `Read` is not guaranteed to fill the buffer. Both truncate the body, so the HMAC never matches and genuine deliveries are rejected | `io.ReadAll` over a `MaxBytesReader` |
| Webhook identity | `userID := event.Email` — the subscription was keyed on a provider-supplied email address. One customer's callback could mutate another account's subscription, and it created records keyed by email rather than user ID | Resolve via the transaction reference we issued, or the user ID we sent in metadata; reject if neither matches |
| Webhook idempotency | The event was marked processed *before* it was applied, and an empty event ID collapsed every delivery onto one key — so the second distinct event was silently dropped as a "duplicate" | Claim only after resolution; empty IDs are not claimable; the key is event type + provider object ID, since one object ID recurs across event types |
| Webhook merge | The handler replaced the whole subscription, so a lifecycle event without a plan or reference erased the correlation data | Merge; only overwrite fields the event carries |
| User identity | `UpsertUser` stored users keyed by email and returned the first email match without ever linking the Google subject, so the `sub` was effectively discarded and an email change created a second account | Users keyed by internal ID with `google_sub` and email indexes; first Google sign-in links the subject to an existing email account |
| `MemoryBusinessCache` | Unsynchronised map reachable from concurrent handlers — a data race | Mutex-guarded |
| Review listing | An unknown business returned `200` with an empty list, indistinguishable from a business with no reviews. Invalid `page`/`limit` were silently coerced | `404` for unknown business; `400` for invalid paging |
| Review validation | A review could be created with no `user_id` and an unbounded body | `user_id` required, body capped |
| Cache TTL | `CacheTTL` was passed to Redis unchecked, and go-redis treats a zero expiration as **no expiry**. An unset TTL — or `REDIS_BUSINESS_TTL_SECONDS=0` — would have cached every business permanently, silently removing the backstop that bounds how long a failed invalidation can serve stale data | Non-positive TTL falls back to a positive default |
| Failed writes | A review write that failed still invalidated the cache, turning a write that never happened into avoidable load on the store | Invalidate only after a successful write |
| Error leakage | Rejected sign-ins and unprocessable webhooks echoed internal detail (which validation rule failed, parser errors) back to the caller | Fixed client-facing messages; detail is logged |
| `.env.example` | Referenced by both the README and `make setup`, but absent from the repository | Added |

---

## 2. What changed and why

**Cache is treated as non-authoritative** ([business.go](internal/service/business.go)). A read or write failure is logged and falls through to the store rather than failing the request. Invalidation runs on a context detached from the request (`context.WithoutCancel`) because the write has already committed — a client that disconnects must not leave a stale entry behind. Each cache call carries its own short timeout so a wedged Redis cannot consume the whole request budget. A failed invalidation is logged at error level and bounded by the TTL, so the worst case is bounded staleness, never divergence.

**Provider errors are separated from caller errors.** `ErrInvalidToken` vs `ErrProviderUnavailable` in auth, `ErrProvider` vs `ErrInvalidSignature` in payments. This is what lets the API answer `401` versus `502`: telling a legitimate user their credentials are invalid because Google was down is a bad failure mode, and it hides the outage from monitoring.

**Google verification** ([google.go](internal/auth/google.go)) validates issuer, audience against the configured client ID, expiry with a small skew allowance, a non-empty `sub`, and a present *and verified* email. The audience check is the one that stops an ID token minted for another application being replayed here — so a missing client ID fails closed rather than comparing against an empty string. Claims decode from both the string form tokeninfo returns and the native form a decoded JWT carries.

**Paystack** ([paystack.go](internal/payments/paystack.go)) sends the Maoni user ID in `metadata`, which is what Paystack echoes back on webhooks and how a callback finds its user. Signatures are HMAC-SHA512 over the raw body, compared in constant time, failing closed when no secret is configured.

**API layer** ([server.go](internal/api/server.go)): `Recover`, `RequestID` and a body limit; framework errors (404/405/415/body-limit) use the same `{"error": ...}` envelope as handlers; `net/http` status constants instead of magic numbers; internal errors are logged with context and never echoed to the client. The review listing now returns `page`, `limit` and `total` alongside `data` so a client can tell whether another page exists.

**Repository interfaces at the service boundary.** Each service now declares the persistence surface it needs (`BusinessRepository`, `SubscriptionRepository`, `UserRepository`) at the point of use, rather than depending on the concrete mock store. This is what "repository" in handler/service/repository actually buys: a MongoDB implementation becomes a drop-in, and failure modes the in-memory store *cannot produce* — write-concern errors, server-selection timeouts — become testable. That boundary is what surfaced the cache-TTL and failed-write defects above. `cmd/api` asserts at compile time that the mock store satisfies all three.

**Everything else stayed put.** Echo, the handler/service/store split, the existing interfaces and the mock store are unchanged. `GetBusiness` and `GetBusinessRaw` are now the same lookup; I left both because the provided starter test depends on `GetBusinessRaw`.

---

## 3. Tests added

Starter tests still pass unchanged. The whole suite passes under `-race`.

- **`internal/store`** — one test per reported symptom (documented-ID lookup, save-then-read visibility, fractional average, 1-based pagination with no skips or repeats, stable ordering under identical timestamps), Google-subject account keying and email linking, single-claim event dedupe, reference lookup, and a concurrent-writer race test.
- **`internal/service`** — aggregates after a write, cache invalidation on write, cache-outage degradation (reads still served, writes still durable), invalidation after client disconnect, review validation, webhook resolution by reference and by metadata, **refusal to resolve by email**, idempotent redelivery, distinct event types not colliding, unresolved events staying unclaimed for retry, signature rejection before any state change, field preservation on partial events, and initialize validation.
- **`internal/auth`** — success paths for both claim encodings; rejection of wrong audience, wrong issuer, expiry, missing `sub`, missing or unverified email; provider 400/401 → invalid token; 5xx, malformed JSON and unreachable host → provider unavailable; empty token never reaches the provider; unconfigured client ID fails closed; context deadline honoured.
- **`internal/payments`** — `httptest` assertions on method, path, bearer header, content type and body including `metadata.user_id`; failure modes (non-2xx, `status:false`, malformed JSON, missing fields, empty body); signature accept/reject including tampered body, wrong key, truncated digest and unconfigured secret; event parsing, type→status mapping, string-encoded metadata and event-ID fallbacks.
- **`internal/api`** — end-to-end over `httptest`, including the full reported symptom (create a review, read the business back, see fresh aggregates), paging metadata, validation status codes, auth 401-vs-502 mapping, and webhook handling with **real** signature verification: signed delivery applied, 64 KiB body and chunked body with no `Content-Length` both accepted (the body-read regression), unsigned and tampered deliveries rejected, duplicate delivery acknowledged but not re-applied.
- **`internal/rediscache`** — optional integration tests against the local Docker Redis: round trip preserving derived fields, TTL expiry, and invalidation-on-write against real Redis. They skip when Redis is unreachable or under `-short`, so `go test ./...` stays green without Docker.

- **`internal/api` (end to end)** — `TestSignUpToActiveSubscriptionJourney` and `TestReviewLifecycleJourney` drive the application the way a client does: over HTTP, through the real router, services, store and cache, with the **real** Google verifier and the **real** Paystack client. Only the two external HTTP endpoints are stood in for, so token validation, signature verification, idempotency and cache invalidation are genuinely exercised rather than faked. The journey covers first sign-in, an email change keeping the same account, four classes of rejected token, checkout reaching Paystack with the right auth and metadata, a tampered webhook, activation, triple redelivery, cancellation preserving correlation data, and another user's callback failing to touch the subscription. The review journey asserts exact aggregates after *every* write and that paging covers every record exactly once.
- **Concurrency (`internal/service`, `internal/api`)** — run under `-race`: 32 simultaneous redeliveries of one webhook apply exactly once; 16 distinct concurrent events all apply; 40 concurrent review writes leave exact aggregates and no lost records; mixed traffic across every endpoint never returns 5xx.
- **`internal/config`** — defaults, environment overrides, malformed numbers falling back rather than becoming zero, `.env` parsing including quoted values, and a real environment variable winning over the file.

No test requires network access or real credentials.

### Verifying the tests actually catch the bugs

A test that never fails proves nothing, so I re-introduced each defect and confirmed the suite goes red. All 15 mutations were caught:

| Mutation | Caught |
|---|---|
| Business lookup back to matching on `Slug` | ✅ |
| Review saved to the `"review"` collection again | ✅ |
| `float64(total / count)` integer division restored | ✅ |
| Pagination offset back to `page * limit` | ✅ |
| Sort tiebreaker removed (unstable ordering) | ✅ |
| Cache invalidation on write removed | ✅ |
| Webhook body back to `Content-Length` + single `Read` | ✅ |
| Webhook identity resolved from customer email | ✅ |
| Idempotency claim removed | ✅ |
| Event claimed before it resolves | ✅ |
| HMAC signature comparison bypassed | ✅ |
| Google audience check bypassed | ✅ |
| Google expiry check bypassed | ✅ |
| Unverified email accepted | ✅ |
| Zero cache TTL passed through to Redis | ✅ |

---

## 4. Trade-offs and assumptions

- **Tokeninfo vs local JWK validation.** I kept the injectable tokeninfo endpoint because the exercise ships it and it makes the flow testable without credentials. **In production I would switch to local ID-token validation against Google's JWKs** (`google.golang.org/api/idtoken`): it removes a network round trip and a hard dependency on Google's availability from the login path, and it avoids putting the ID token in a URL query string where it can land in access logs and proxy traces. The validation rules implemented here (issuer, audience, expiry, `sub`, verified email) are the same either way, so the swap is contained to `fetchTokenInfo`.
- **The average is returned exactly** (`4.333333333333333`) rather than rounded. Rounding is a presentation decision and I did not want to bake one into the API without a product answer; the fix was the truncation, not the number of decimals.
- **Webhooks update existing subscriptions; they never create one from provider-supplied identity.** An event that matches no local subscription is rejected rather than guessed at. This is the conservative direction: it is recoverable (the provider retries, and the retry is not consumed) whereas a wrong guess silently corrupts another user's account.
- **A new checkout does not downgrade an active subscriber to `pending`**, so starting a second payment cannot revoke access the customer already has.
- **Amount must be positive.** The original accepted `0`; Paystack rejects it, so catching it locally turns a 502 into a 400.
- **Idempotency keys live in the same in-memory store as everything else** and reset with the process, matching the exercise's mock-data constraint.
- **Event types implemented:** `charge.success`, `subscription.create`, `subscription.disable`, `subscription.not_renew`, `invoice.payment_failed`. Unrecognised events are acknowledged with `200` so Paystack stops retrying something we deliberately ignore. In production I would also handle `invoice.create` and `invoice.update` (renewal notices), `subscription.expiring_cards`, `charge.dispute.*`, and `transfer.*` if payouts are in scope.
- I did not add authentication to review creation, since no auth middleware exists in the exercise and adding one would be a redesign rather than a fix. It is called out below.

---

## 5. Known remaining issues

- **Cache-aside read/write race.** A reader that loads from the store just before a concurrent write can repopulate the cache *after* the invalidation, re-introducing a stale entry until the TTL expires. Bounded, not eliminated. With a real database I would either version the cache entry and write conditionally, or use a short-TTL "delete + hold" marker.
- **`review_count` / `average_rating` are recomputed by scanning every review** on each cache miss. Fine for a mock store; against Mongo it should be a maintained counter (`$inc` and a running sum) or an aggregation with an index on `business_id`.
- **`CreateReview` is unauthenticated** and does not check that `user_id` is a real user or that the user has not already reviewed the business.
- **Concurrent webhooks for the same user read-modify-write the subscription.** Deduplication is atomic (a single claim per event ID), but two *different* events arriving at once both read the current record and the later write wins. The mock store has no transactions; against Mongo this is a single conditional update rather than read-then-write.
- **Webhook ordering.** Events carry no local sequence, so an out-of-order delivery (a stale `charge.success` arriving after `subscription.disable`) would apply the older state. Production needs the provider timestamp compared against `updated_at`, or a state machine that refuses invalid transitions.
- **`GetBusiness` and `GetBusinessRaw` are now duplicates**; I left both to keep the starter test compiling. They should collapse into one method.
- **The repository interfaces mirror the mock store's signatures**, so `PutSubscription` and `MarkEventProcessed` return no error. A MongoDB implementation would need error returns on both, and `MarkEventProcessed` would become an upsert against a unique index rather than a map write.
- **Subscription lifecycle is a single record per user**, so historical transitions are not retained. A real implementation wants an append-only subscription-event log.
- The idempotency set grows unbounded in memory; in Mongo this would be a collection with a unique index on the event ID and a TTL index to expire old keys.

---

## 6. What I would do next with more time

1. Swap tokeninfo for local JWK-based ID-token validation, keeping the interface identical.
2. Persist to MongoDB behind a repository interface, and make the webhook apply-and-mark a single transaction so idempotency and the state change commit together.
3. Add authentication middleware and per-user rate limiting on review creation.
4. Replay-protect webhooks with a timestamp window in addition to the signature, and record every raw delivery for audit before processing.
5. Contract tests against Paystack's sandbox, run outside the unit suite.
6. Add `Retry-After` and a small backoff-aware client for provider calls, plus a circuit breaker so a Paystack outage degrades checkout rather than saturating request threads.

---

## 7. Monitoring in production

**Cross-cutting:** RED metrics (rate, errors, duration) per route with status-class breakdown; trace IDs propagated from the `X-Request-ID` middleware into every provider call and log line so one identifier links an API request to the Google and Paystack calls it made.

**Review flow**
- Alert on `5xx` rate and p99 latency for `POST /businesses/:id/reviews`.
- Counter for reviews created versus cache invalidations attempted/failed. A gap between them is the stale-data symptom appearing before customers report it.
- A periodic consistency job comparing each business's stored `review_count` against a count of its reviews; any drift is a data-integrity alarm, not a cache issue.

**Google auth**
- Separate counters for `invalid_token` and `provider_unavailable`. A rise in the first is usually a client or clock problem; a rise in the second is Google or our network, and only the second should page.
- Latency histogram and timeout rate for the verification call, with an alert on sign-in success ratio dropping below its baseline — that catches a misconfigured client ID (every token fails the audience check) faster than any single error counter.
- Alert on the process starting with no `GOOGLE_CLIENT_ID`; it is logged at startup today.

**Redis**
- Hit/miss ratio, operation latency, and error rate from the cache wrapper — a miss ratio near 1.0 means the cache is silently useless, which the current fail-open behaviour would otherwise hide completely.
- The `/health` endpoint already reports connectivity; that feeds a low-severity alert, not a pager, since the API is designed to serve without Redis.
- Redis-side: memory usage, evicted keys and connection count. Evictions before TTL expiry mean entries are disappearing early and load is shifting to the store.

**Paystack**
- Initialize: success rate, latency, and non-2xx breakdown by status; alert on sustained `ErrProvider`.
- Webhooks: deliveries received, signature failures, duplicates skipped, and *unresolved* events. **Signature failures and unresolved events should page** — the first can mean a leaked or rotated secret, the second means money moved without a subscription being updated.
- A reconciliation job comparing Paystack transactions against local subscription state over a rolling window, alerting on any transaction with no corresponding local change. Webhooks get lost; reconciliation is what makes that recoverable.
- Business-level alerting on the ratio of `charge.success` to `invoice.payment_failed`, which surfaces upstream payment problems that no error rate would show.
