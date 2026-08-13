package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

// These tests drive the application the way a client does: over HTTP, through
// the real router, services, store and cache, with the real Google verifier
// and the real Paystack client. Only the two external HTTP endpoints are
// stood in for by httptest servers, so everything we own — signature
// verification, token validation, idempotency, cache invalidation — is
// genuinely exercised rather than faked.

const (
	e2eClientID = "maoni-e2e.apps.googleusercontent.com"
	e2eSecret   = "sk_test_e2e_not_a_real_key"
)

type e2eEnv struct {
	t        *testing.T
	client   *http.Client
	baseURL  string
	store    *store.MemoryStore
	paystack *fakePaystack
	google   *fakeGoogle
	// auth and subs are exposed so a test can swap in a double for a failure
	// mode the fake provider servers cannot produce.
	auth *service.AuthService
	subs *service.SubscriptionService
}

// fakeGoogle stands in for oauth2.googleapis.com/tokeninfo. Tokens it does not
// know are rejected the way Google rejects them: HTTP 400.
type fakeGoogle struct {
	mu     sync.Mutex
	claims map[string]map[string]any
	server *httptest.Server
}

func newFakeGoogle(t *testing.T) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{claims: map[string]map[string]any{}}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		claims, ok := g.claims[r.URL.Query().Get("id_token")]
		g.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_token"})
			return
		}
		_ = json.NewEncoder(w).Encode(claims)
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *fakeGoogle) issue(token, sub, email, name string) {
	g.set(token, map[string]any{
		"iss": "https://accounts.google.com", "aud": e2eClientID,
		"sub": sub, "email": email, "email_verified": "true", "name": name,
		"exp": fmt.Sprint(time.Now().Add(time.Hour).Unix()),
	})
}

func (g *fakeGoogle) set(token string, claims map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.claims[token] = claims
}

// fakePaystack stands in for api.paystack.co and records what we sent it.
type fakePaystack struct {
	mu       sync.Mutex
	requests []map[string]any
	authHdrs []string
	server   *httptest.Server
}

func newFakePaystack(t *testing.T) *fakePaystack {
	t.Helper()
	p := &fakePaystack{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)

		p.mu.Lock()
		p.requests = append(p.requests, body)
		p.authHdrs = append(p.authHdrs, r.Header.Get("Authorization"))
		p.mu.Unlock()

		// Paystack echoes the reference it was given.
		reference, _ := body["reference"].(string)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true, "message": "Authorization URL created",
			"data": map[string]any{
				"authorization_url": "https://checkout.paystack.com/e2e" + reference,
				"access_code":       "acc_e2e",
				"reference":         reference,
			},
		})
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakePaystack) lastRequest(t *testing.T) (map[string]any, string) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		t.Fatal("paystack was never called")
	}
	return p.requests[len(p.requests)-1], p.authHdrs[len(p.authHdrs)-1]
}

func newE2E(t *testing.T) *e2eEnv { return newE2EWithRedis(t, false) }

func newE2EWithRedis(t *testing.T, redisHealthy bool) *e2eEnv {
	t.Helper()
	google := newFakeGoogle(t)
	paystack := newFakePaystack(t)

	st := store.NewMemoryStore()
	businessCache := cache.NewMemoryBusinessCache()

	authSvc := &service.AuthService{Store: st, Verifier: &auth.GoogleVerifier{
		ClientID:     e2eClientID,
		TokenInfoURL: google.server.URL + "/tokeninfo",
		HTTPClient:   &http.Client{Timeout: 2 * time.Second},
	}}
	subs := &service.SubscriptionService{Store: st, Provider: &payments.PaystackClient{
		SecretKey:  e2eSecret,
		BaseURL:    paystack.server.URL,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	}}
	srv := api.New(
		&service.BusinessService{Store: st, Cache: businessCache, CacheTTL: time.Minute},
		authSvc, subs,
		func() bool { return redisHealthy },
	)
	ts := httptest.NewServer(srv.Echo)
	t.Cleanup(ts.Close)

	return &e2eEnv{
		t: t, client: ts.Client(), baseURL: ts.URL,
		store: st, paystack: paystack, google: google,
		auth: authSvc, subs: subs,
	}
}

func (e *e2eEnv) request(method, path string, body []byte, headers map[string]string) (int, map[string]any, http.Header) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	return e.requestReader(method, path, reader, headers)
}

// requestReader sends an arbitrary reader as the body. Passing a reader whose
// concrete type net/http cannot size (io.NopCloser, say) makes it send the
// request chunked, which is what the webhook framing test needs.
func (e *e2eEnv) requestReader(method, path string, body io.Reader, headers map[string]string) (int, map[string]any, http.Header) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, e.baseURL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			e.t.Fatalf("%s %s returned non-JSON %q", method, path, raw)
		}
	}
	return resp.StatusCode, decoded, resp.Header
}

// seedPendingSubscription drives the real checkout path and returns the
// transaction reference Paystack echoed back.
func (e *e2eEnv) seedPendingSubscription(t *testing.T, userID string) string {
	t.Helper()
	resp, err := e.subs.Initialize(context.Background(), userID, "user@example.com", "PLN_x", 500000)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Reference
}

func e2eSign(body []byte) string {
	mac := hmac.New(sha512.New, []byte(e2eSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// TestSignUpToActiveSubscriptionJourney walks one user from first sign-in to an
// active subscription and out again, the way the real system would.
func TestSignUpToActiveSubscriptionJourney(t *testing.T) {
	e := newE2E(t)
	e.google.issue("tok-ada", "google-sub-ada", "ada@example.com", "Ada Lovelace")

	var userID string

	t.Run("01 health reports cache state without failing", func(t *testing.T) {
		status, body, _ := e.request(http.MethodGet, "/health", nil, nil)
		if status != http.StatusOK || body["status"] != "ok" {
			t.Fatalf("status=%d body=%v", status, body)
		}
		// Redis is deliberately absent here: the API must still be healthy.
		if body["redis"] != false {
			t.Fatalf("redis = %v, want false", body["redis"])
		}
	})

	t.Run("02 google sign-in creates the account", func(t *testing.T) {
		status, body, hdr := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-ada"}`), nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %v", status, body)
		}
		user, _ := body["user"].(map[string]any)
		if user["google_id"] != "google-sub-ada" || user["email"] != "ada@example.com" {
			t.Fatalf("unexpected user %v", user)
		}
		userID, _ = user["id"].(string)
		if userID == "" {
			t.Fatal("no user id returned")
		}
		if hdr.Get("X-Request-Id") == "" {
			t.Fatal("no request ID on the response; correlation middleware is not wired")
		}
	})

	t.Run("03 changed email keeps the same account", func(t *testing.T) {
		// Same Google subject, new email — the account must follow the sub.
		e.google.issue("tok-ada", "google-sub-ada", "ada.lovelace@example.com", "Ada Lovelace")

		status, body, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-ada"}`), nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %v", status, body)
		}
		user, _ := body["user"].(map[string]any)
		if user["id"] != userID {
			t.Fatalf("a changed email created a second account: %v vs %s", user["id"], userID)
		}
		if user["email"] != "ada.lovelace@example.com" {
			t.Fatalf("email was not updated: %v", user)
		}
	})

	t.Run("04 unknown and mis-issued tokens are rejected", func(t *testing.T) {
		// Unknown to Google: rejected upstream with 400.
		if status, _, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-unknown"}`), nil); status != http.StatusUnauthorized {
			t.Fatalf("unknown token status = %d, want 401", status)
		}
		// Valid token, wrong audience: this is the replay case, and it is our
		// check rather than Google's that has to catch it.
		e.google.set("tok-other-app", map[string]any{
			"iss": "https://accounts.google.com", "aud": "someone-else.apps.googleusercontent.com",
			"sub": "google-sub-mallory", "email": "mallory@example.com", "email_verified": "true",
			"exp": fmt.Sprint(time.Now().Add(time.Hour).Unix()),
		})
		if status, _, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-other-app"}`), nil); status != http.StatusUnauthorized {
			t.Fatalf("foreign-audience token status = %d, want 401", status)
		}
		// Expired token.
		e.google.set("tok-expired", map[string]any{
			"iss": "https://accounts.google.com", "aud": e2eClientID,
			"sub": "google-sub-ada", "email": "ada@example.com", "email_verified": "true",
			"exp": fmt.Sprint(time.Now().Add(-time.Hour).Unix()),
		})
		if status, _, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-expired"}`), nil); status != http.StatusUnauthorized {
			t.Fatalf("expired token status = %d, want 401", status)
		}
		// An unverified email must not become an account.
		e.google.set("tok-unverified", map[string]any{
			"iss": "https://accounts.google.com", "aud": e2eClientID,
			"sub": "google-sub-eve", "email": "eve@example.com", "email_verified": "false",
			"exp": fmt.Sprint(time.Now().Add(time.Hour).Unix()),
		})
		if status, _, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-unverified"}`), nil); status != http.StatusUnauthorized {
			t.Fatalf("unverified-email token status = %d, want 401", status)
		}
	})

	var reference string

	t.Run("05 subscription initialize reaches paystack correctly", func(t *testing.T) {
		payload := fmt.Sprintf(`{"user_id":%q,"email":"ada.lovelace@example.com","plan_code":"PLN_maoni_pro","amount":500000}`, userID)
		status, body, _ := e.request(http.MethodPost, "/api/v1/subscriptions/initialize", []byte(payload), nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d: %v", status, body)
		}
		if body["authorization_url"] == "" || body["reference"] == "" {
			t.Fatalf("unusable checkout response %v", body)
		}
		reference, _ = body["reference"].(string)

		sent, authHeader := e.paystack.lastRequest(t)
		if authHeader != "Bearer "+e2eSecret {
			t.Fatalf("provider auth header = %q", authHeader)
		}
		if sent["plan"] != "PLN_maoni_pro" {
			t.Fatalf("plan not forwarded: %v", sent)
		}
		if amount, _ := sent["amount"].(float64); int64(amount) != 500000 {
			t.Fatalf("amount = %v", sent["amount"])
		}
		// The correlation that makes the webhook resolvable.
		meta, _ := sent["metadata"].(map[string]any)
		if meta["user_id"] != userID {
			t.Fatalf("metadata.user_id = %v, want %s", meta["user_id"], userID)
		}
	})

	t.Run("06 subscription starts pending", func(t *testing.T) {
		status, body, _ := e.request(http.MethodGet, "/api/v1/subscriptions/"+userID, nil, nil)
		if status != http.StatusOK || body["status"] != "pending" {
			t.Fatalf("status=%d body=%v", status, body)
		}
	})

	chargeSuccess := func() []byte {
		return []byte(fmt.Sprintf(`{"event":"charge.success","data":{"id":9001,"reference":%q,"status":"success",`+
			`"customer":{"email":"ada.lovelace@example.com"},"plan":{"plan_code":"PLN_maoni_pro"},`+
			`"metadata":{"user_id":%q}}}`, reference, userID))
	}

	t.Run("07 tampered webhook is rejected", func(t *testing.T) {
		body := chargeSuccess()
		tampered := append(body[:len(body)-1], []byte(` }`)...) // same signature, different bytes
		status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", tampered,
			map[string]string{"x-paystack-signature": e2eSign(body)})
		if status != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", status)
		}
		if sub, _ := e.store.GetSubscription(userID); sub.Status != "pending" {
			t.Fatalf("a rejected webhook changed state: %+v", sub)
		}
	})

	t.Run("08 signed webhook activates the subscription", func(t *testing.T) {
		body := chargeSuccess()
		status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", body,
			map[string]string{"x-paystack-signature": e2eSign(body)})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}

		status, sub, _ := e.request(http.MethodGet, "/api/v1/subscriptions/"+userID, nil, nil)
		if status != http.StatusOK || sub["status"] != "active" {
			t.Fatalf("status=%d sub=%v", status, sub)
		}
		if sub["plan_code"] != "PLN_maoni_pro" {
			t.Fatalf("plan lost: %v", sub)
		}
	})

	t.Run("09 redelivery is acknowledged but not re-applied", func(t *testing.T) {
		before, _ := e.store.GetSubscription(userID)

		body := chargeSuccess()
		for i := 0; i < 3; i++ {
			status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", body,
				map[string]string{"x-paystack-signature": e2eSign(body)})
			if status != http.StatusOK {
				t.Fatalf("redelivery %d: status = %d, want 200", i+1, status)
			}
		}

		after, _ := e.store.GetSubscription(userID)
		if !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("redelivery re-applied the event: %v -> %v", before.UpdatedAt, after.UpdatedAt)
		}
	})

	t.Run("10 cancellation applies and preserves correlation data", func(t *testing.T) {
		// A subscription lifecycle event: no transaction reference, and it must
		// resolve through the metadata user ID instead.
		body := []byte(fmt.Sprintf(`{"event":"subscription.disable","data":{"id":9002,`+
			`"subscription_code":"SUB_e2e","customer":{"email":"ada.lovelace@example.com"},`+
			`"metadata":{"user_id":%q}}}`, userID))
		status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", body,
			map[string]string{"x-paystack-signature": e2eSign(body)})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}

		_, sub, _ := e.request(http.MethodGet, "/api/v1/subscriptions/"+userID, nil, nil)
		if sub["status"] != "cancelled" {
			t.Fatalf("status = %v, want cancelled", sub["status"])
		}
		// The cancellation carried no plan or reference; neither may be lost.
		if sub["plan_code"] != "PLN_maoni_pro" || sub["reference"] != reference {
			t.Fatalf("correlation data erased on cancellation: %v", sub)
		}
	})

	t.Run("11 another user's callback cannot touch this subscription", func(t *testing.T) {
		before, _ := e.store.GetSubscription(userID)

		// Same customer email, but a reference and user ID we know nothing
		// about. Resolving on email would hand this event Ada's subscription.
		body := []byte(`{"event":"charge.success","data":{"id":9003,"reference":"ref_not_ours","status":"success",` +
			`"customer":{"email":"ada.lovelace@example.com"},"metadata":{"user_id":"usr_someone_else"}}}`)
		status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", body,
			map[string]string{"x-paystack-signature": e2eSign(body)})
		if status != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", status)
		}

		after, _ := e.store.GetSubscription(userID)
		if after != before {
			t.Fatalf("an unrelated callback mutated the subscription: %+v -> %+v", before, after)
		}
	})
}

// TestReviewLifecycleJourney checks the read/write/cache path the way a client
// experiences it, including that paging stays complete while data changes.
func TestReviewLifecycleJourney(t *testing.T) {
	e := newE2E(t)

	t.Run("01 seeded aggregates are exact", func(t *testing.T) {
		status, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if count, _ := body["review_count"].(float64); int(count) != 3 {
			t.Fatalf("review_count = %v", body["review_count"])
		}
		if avg, _ := body["average_rating"].(float64); avg < 4.333 || avg > 4.334 {
			t.Fatalf("average_rating = %v, want 13/3", body["average_rating"])
		}
	})

	t.Run("02 writes are visible immediately and aggregates stay exact", func(t *testing.T) {
		total, sum := 3, 13
		for i, rating := range []int{1, 2, 5, 5, 3} {
			payload := fmt.Sprintf(`{"user_id":"user_%d","rating":%d,"body":"review %d"}`, i, rating, i)
			status, _, _ := e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(payload), nil)
			if status != http.StatusCreated {
				t.Fatalf("review %d: status = %d", i, status)
			}
			total, sum = total+1, sum+rating

			// Read back after every single write: no staleness window.
			_, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
			if count, _ := body["review_count"].(float64); int(count) != total {
				t.Fatalf("after write %d: review_count = %v, want %d", i, body["review_count"], total)
			}
			want := float64(sum) / float64(total)
			if avg, _ := body["average_rating"].(float64); avg != want {
				t.Fatalf("after write %d: average_rating = %v, want %v", i, avg, want)
			}
		}
	})

	t.Run("03 paging covers every review exactly once", func(t *testing.T) {
		seen := map[string]int{}
		var expectedTotal int
		for page := 1; ; page++ {
			_, body, _ := e.request(http.MethodGet,
				fmt.Sprintf("/api/v1/businesses/biz_1/reviews?page=%d&limit=3", page), nil, nil)
			expectedTotal, _ = intOf(body["total"])
			data, _ := body["data"].([]any)
			if len(data) == 0 {
				break
			}
			for _, item := range data {
				review, _ := item.(map[string]any)
				id, _ := review["id"].(string)
				seen[id]++
			}
			if page > 20 {
				t.Fatal("pagination did not terminate")
			}
		}
		if len(seen) != expectedTotal {
			t.Fatalf("paged over %d distinct reviews, total says %d", len(seen), expectedTotal)
		}
		for id, n := range seen {
			if n != 1 {
				t.Fatalf("review %s appeared %d times across pages", id, n)
			}
		}
	})

	t.Run("04 reviews are newest first", func(t *testing.T) {
		_, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews?limit=100", nil, nil)
		data, _ := body["data"].([]any)
		var previous time.Time
		for i, item := range data {
			review, _ := item.(map[string]any)
			createdAt, err := time.Parse(time.RFC3339Nano, review["created_at"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if i > 0 && createdAt.After(previous) {
				t.Fatalf("review %d is newer than the one before it", i)
			}
			previous = createdAt
		}
	})
}

func intOf(v any) (int, bool) {
	f, ok := v.(float64)
	return int(f), ok
}
