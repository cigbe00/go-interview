package api_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

const webhookSecret = "sk_test_not_a_real_key"

type harness struct {
	t     *testing.T
	url   string
	store *store.MemoryStore
	auth  *service.AuthService
	subs  *service.SubscriptionService
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := store.NewMemoryStore()
	authSvc := &service.AuthService{
		Store:    st,
		Verifier: auth.FakeVerifier{Identity: auth.Identity{Subject: "google-1", Email: "user@example.com", Name: "Test User"}},
	}
	// A real Paystack client so signature verification and payload parsing are
	// exercised end to end; only the network call in Initialize is faked.
	subs := &service.SubscriptionService{Store: st, Provider: &payments.PaystackClient{SecretKey: webhookSecret}}
	srv := api.New(
		&service.BusinessService{Store: st, Cache: cache.NewMemoryBusinessCache(), CacheTTL: time.Minute},
		authSvc, subs, func() bool { return true },
	)
	ts := httptest.NewServer(srv.Echo)
	t.Cleanup(ts.Close)
	return &harness{t: t, url: ts.URL, store: st, auth: authSvc, subs: subs}
}

func (h *harness) do(method, path string, body io.Reader, headers map[string]string) (int, []byte) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, h.url+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, raw
}

func (h *harness) getJSON(path string) (int, map[string]any) {
	h.t.Helper()
	status, raw := h.do(http.MethodGet, path, nil, nil)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("GET %s returned non-JSON %q: %v", path, raw, err)
		}
	}
	return status, out
}

func (h *harness) postJSON(path, body string) (int, map[string]any) {
	h.t.Helper()
	status, raw := h.do(http.MethodPost, path, strings.NewReader(body), nil)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			h.t.Fatalf("POST %s returned non-JSON %q: %v", path, raw, err)
		}
	}
	return status, out
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	status, body := h.getJSON("/health")
	if status != http.StatusOK || body["status"] != "ok" || body["redis"] != true {
		t.Fatalf("status=%d body=%v", status, body)
	}
}

func TestGetBusinessByDocumentedID(t *testing.T) {
	h := newHarness(t)

	status, body := h.getJSON("/api/v1/businesses/biz_1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["id"] != "biz_1" || body["name"] != "Lagos Bistro" {
		t.Fatalf("unexpected body %v", body)
	}
	if count, _ := body["review_count"].(float64); int(count) != 3 {
		t.Fatalf("review_count = %v, want 3", body["review_count"])
	}
	// 5+4+4 over three reviews: the average must not be truncated to 4.
	if avg, _ := body["average_rating"].(float64); avg < 4.33 || avg > 4.34 {
		t.Fatalf("average_rating = %v, want ~4.333", body["average_rating"])
	}

	if status, _ := h.getJSON("/api/v1/businesses/nope"); status != http.StatusNotFound {
		t.Fatalf("unknown business status = %d, want 404", status)
	}
}

// The full reported symptom, over HTTP: create a review, then read the
// business back and see the new aggregates rather than a cached copy.
func TestCreateReviewIsReflectedInBusinessImmediately(t *testing.T) {
	h := newHarness(t)

	if status, _ := h.getJSON("/api/v1/businesses/biz_1"); status != http.StatusOK {
		t.Fatalf("warm-up read status = %d", status)
	}

	status, created := h.postJSON("/api/v1/businesses/biz_1/reviews", `{"user_id":"user_99","rating":1,"body":"Not great"}`)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", status, created)
	}
	if created["id"] == "" || created["business_id"] != "biz_1" {
		t.Fatalf("unexpected review %v", created)
	}

	_, body := h.getJSON("/api/v1/businesses/biz_1")
	if count, _ := body["review_count"].(float64); int(count) != 4 {
		t.Fatalf("review_count = %v, want 4 (stale cache?)", body["review_count"])
	}
	if avg, _ := body["average_rating"].(float64); avg != 3.5 {
		t.Fatalf("average_rating = %v, want 3.5", body["average_rating"])
	}
}

func TestListReviewsPagination(t *testing.T) {
	h := newHarness(t)

	status, body := h.getJSON("/api/v1/businesses/biz_1/reviews?page=1&limit=2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	data, _ := body["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("page 1 returned %d reviews, want 2: %v", len(data), body)
	}
	first, _ := data[0].(map[string]any)
	if first["id"] != "rev_3" {
		t.Fatalf("page 1 starts at %v, want the newest review rev_3", first["id"])
	}
	if total, _ := body["total"].(float64); int(total) != 3 {
		t.Fatalf("total = %v, want 3", body["total"])
	}

	_, page2 := h.getJSON("/api/v1/businesses/biz_1/reviews?page=2&limit=2")
	data2, _ := page2["data"].([]any)
	if len(data2) != 1 {
		t.Fatalf("page 2 returned %d reviews, want 1", len(data2))
	}

	// Defaults apply when the parameters are omitted.
	_, defaults := h.getJSON("/api/v1/businesses/biz_1/reviews")
	if len(defaults["data"].([]any)) != 3 {
		t.Fatalf("default page = %v", defaults)
	}
}

func TestListReviewsRequestValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		path string
		want int
	}{
		{"/api/v1/businesses/biz_1/reviews?page=0", http.StatusBadRequest},
		{"/api/v1/businesses/biz_1/reviews?page=-1", http.StatusBadRequest},
		{"/api/v1/businesses/biz_1/reviews?page=abc", http.StatusBadRequest},
		{"/api/v1/businesses/biz_1/reviews?limit=0", http.StatusBadRequest},
		{"/api/v1/businesses/biz_1/reviews?limit=1000", http.StatusBadRequest},
		{"/api/v1/businesses/nope/reviews", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if status, _ := h.getJSON(tc.path); status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
		})
	}
}

func TestCreateReviewRequestValidation(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"rating above range", "/api/v1/businesses/biz_1/reviews", `{"user_id":"u","rating":9,"body":"x"}`, http.StatusBadRequest},
		{"rating below range", "/api/v1/businesses/biz_1/reviews", `{"user_id":"u","rating":0,"body":"x"}`, http.StatusBadRequest},
		{"missing user", "/api/v1/businesses/biz_1/reviews", `{"rating":5,"body":"x"}`, http.StatusBadRequest},
		{"malformed json", "/api/v1/businesses/biz_1/reviews", `{"rating":`, http.StatusBadRequest},
		{"wrong field type", "/api/v1/businesses/biz_1/reviews", `{"user_id":"u","rating":"five"}`, http.StatusBadRequest},
		{"unknown business", "/api/v1/businesses/nope/reviews", `{"user_id":"u","rating":5,"body":"x"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := h.postJSON(tc.path, tc.body)
			if status != tc.want {
				t.Fatalf("status = %d, want %d (%v)", status, tc.want, body)
			}
			if body["error"] == nil {
				t.Fatalf("error responses must carry an error field: %v", body)
			}
		})
	}
}

func TestGoogleAuthStatusMapping(t *testing.T) {
	h := newHarness(t)

	status, body := h.postJSON("/api/v1/auth/google", `{"id_token":"tok"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["google_id"] != "google-1" || user["email"] != "user@example.com" {
		t.Fatalf("unexpected user %v", body)
	}

	// A rejected credential is the caller's problem: 401.
	h.auth.Verifier = auth.FakeVerifier{Err: auth.ErrInvalidToken}
	if status, _ := h.postJSON("/api/v1/auth/google", `{"id_token":"tok"}`); status != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", status)
	}

	// A provider outage is ours: 502, not a false "your login is invalid".
	h.auth.Verifier = auth.FakeVerifier{Err: auth.ErrProviderUnavailable}
	if status, _ := h.postJSON("/api/v1/auth/google", `{"id_token":"tok"}`); status != http.StatusBadGateway {
		t.Fatalf("provider outage status = %d, want 502", status)
	}
}

func TestSubscriptionInitializeValidation(t *testing.T) {
	h := newHarness(t)
	h.subs.Provider = payments.FakeProvider{InitErr: payments.ErrProvider}

	// Bad input is rejected before the provider is called.
	if status, _ := h.postJSON("/api/v1/subscriptions/initialize", `{"email":"user@example.com","amount":5000}`); status != http.StatusBadRequest {
		t.Fatalf("missing user_id status = %d, want 400", status)
	}
	// A provider failure is an upstream problem, not a client error.
	if status, _ := h.postJSON("/api/v1/subscriptions/initialize", `{"user_id":"user_1","email":"user@example.com","amount":5000}`); status != http.StatusBadGateway {
		t.Fatalf("provider failure status = %d, want 502", status)
	}

	h.subs.Provider = payments.FakeProvider{InitResp: payments.InitializeResponse{
		AuthorizationURL: "https://checkout.paystack.com/abc", AccessCode: "abc", Reference: "ref_1",
	}}
	status, body := h.postJSON("/api/v1/subscriptions/initialize", `{"user_id":"user_1","email":"user@example.com","plan_code":"PLN_x","amount":5000}`)
	if status != http.StatusOK || body["reference"] != "ref_1" {
		t.Fatalf("status = %d body = %v", status, body)
	}

	status, sub := h.getJSON("/api/v1/subscriptions/user_1")
	if status != http.StatusOK || sub["status"] != "pending" {
		t.Fatalf("status = %d sub = %v", status, sub)
	}
	if status, _ := h.getJSON("/api/v1/subscriptions/nobody"); status != http.StatusNotFound {
		t.Fatalf("unknown subscription status = %d, want 404", status)
	}
}

func sign(body []byte) string {
	mac := hmac.New(sha512.New, []byte(webhookSecret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *harness) seedPendingSubscription(t *testing.T, userID, reference string) {
	t.Helper()
	h.subs.Provider = payments.FakeProvider{InitResp: payments.InitializeResponse{
		AuthorizationURL: "https://checkout.paystack.com/abc", AccessCode: "abc", Reference: reference,
	}}
	if _, err := h.subs.Initialize(context.Background(), userID, "user@example.com", "PLN_x", 5000); err != nil {
		t.Fatal(err)
	}
	h.subs.Provider = &payments.PaystackClient{SecretKey: webhookSecret}
}

func chargeSuccessPayload(reference, padding string) []byte {
	return chargeSuccessPayloadFor(reference, "user_1", padding)
}

func chargeSuccessPayloadFor(reference, userID, padding string) []byte {
	return []byte(`{"event":"charge.success","data":{"id":42,"reference":"` + reference +
		`","status":"success","customer":{"email":"user@example.com"},"plan":{"plan_code":"PLN_x"},` +
		`"metadata":{"user_id":"` + userID + `"},"log":"` + padding + `"}}`)
}

func TestWebhookAppliesSignedEvent(t *testing.T) {
	h := newHarness(t)
	h.seedPendingSubscription(t, "user_1", "ref_1")

	body := chargeSuccessPayload("ref_1", "")
	status, raw := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewReader(body),
		map[string]string{"x-paystack-signature": sign(body)})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, raw)
	}

	sub, err := h.store.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "active" {
		t.Fatalf("subscription = %+v, want active", sub)
	}
}

// The signature covers the exact bytes received, so the handler has to read the
// body in full. Reading only what Content-Length advertised — or a single
// Read call — truncated large and chunked payloads, which then failed
// verification even though the delivery was genuine.
func TestWebhookReadsWholeBodyRegardlessOfFraming(t *testing.T) {
	t.Run("body spanning multiple reads", func(t *testing.T) {
		h := newHarness(t)
		h.seedPendingSubscription(t, "user_1", "ref_1")

		body := chargeSuccessPayload("ref_1", strings.Repeat("x", 64*1024))
		status, raw := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewReader(body),
			map[string]string{"x-paystack-signature": sign(body)})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", status, raw)
		}
		if sub, _ := h.store.GetSubscription("user_1"); sub.Status != "active" {
			t.Fatalf("subscription = %+v, want active", sub)
		}
	})

	t.Run("chunked body with no content-length", func(t *testing.T) {
		h := newHarness(t)
		h.seedPendingSubscription(t, "user_1", "ref_1")

		body := chargeSuccessPayload("ref_1", "")
		// io.NopCloser hides the concrete reader type, so net/http cannot
		// derive a Content-Length and sends the request chunked.
		status, raw := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", io.NopCloser(bytes.NewReader(body)),
			map[string]string{"x-paystack-signature": sign(body)})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", status, raw)
		}
		if sub, _ := h.store.GetSubscription("user_1"); sub.Status != "active" {
			t.Fatalf("subscription = %+v, want active", sub)
		}
	})
}

func TestWebhookRejectsUnsignedAndUnknownDeliveries(t *testing.T) {
	h := newHarness(t)
	h.seedPendingSubscription(t, "user_1", "ref_1")
	body := chargeSuccessPayload("ref_1", "")

	cases := []struct {
		name      string
		body      []byte
		signature string
		want      int
	}{
		{"missing signature", body, "", http.StatusUnauthorized},
		{"wrong signature", body, sign([]byte("something else")), http.StatusUnauthorized},
		{"signature over a different body", chargeSuccessPayload("ref_1", "tampered"), sign(body), http.StatusUnauthorized},
		{
			"unknown subscription",
			chargeSuccessPayloadFor("ref_unknown", "user_nobody", ""),
			sign(chargeSuccessPayloadFor("ref_unknown", "user_nobody", "")),
			http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewReader(tc.body),
				map[string]string{"x-paystack-signature": tc.signature})
			if status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
			if sub, _ := h.store.GetSubscription("user_1"); sub.Status != "pending" {
				t.Fatalf("rejected delivery changed state: %+v", sub)
			}
		})
	}
}

// A duplicate delivery is acknowledged but must not be applied twice.
func TestWebhookDuplicateDeliveryIsAcknowledgedOnce(t *testing.T) {
	h := newHarness(t)
	h.seedPendingSubscription(t, "user_1", "ref_1")
	body := chargeSuccessPayload("ref_1", "")
	signature := sign(body)

	for i := 0; i < 2; i++ {
		status, raw := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewReader(body),
			map[string]string{"x-paystack-signature": signature})
		if status != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200: %s", i+1, status, raw)
		}
	}

	// Move the subscription on, then replay: the replay must be ignored.
	sub, _ := h.store.GetSubscription("user_1")
	sub.Status = "cancelled"
	h.store.PutSubscription(sub)

	if status, _ := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewReader(body),
		map[string]string{"x-paystack-signature": signature}); status != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", status)
	}
	if sub, _ := h.store.GetSubscription("user_1"); sub.Status != "cancelled" {
		t.Fatalf("replay re-applied the event: %+v", sub)
	}
}

// An event we do not act on is acknowledged so the provider stops retrying it.
func TestWebhookAcknowledgesUnsupportedEvent(t *testing.T) {
	h := newHarness(t)
	h.seedPendingSubscription(t, "user_1", "ref_1")

	body := []byte(`{"event":"customeridentification.failed","data":{"id":7}}`)
	status, _ := h.do(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewReader(body),
		map[string]string{"x-paystack-signature": sign(body)})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if sub, _ := h.store.GetSubscription("user_1"); sub.Status != "pending" {
		t.Fatalf("unsupported event changed state: %+v", sub)
	}
}

// Framework-generated errors must use the same JSON envelope as handlers.
func TestUnroutedRequestsReturnJSONErrors(t *testing.T) {
	h := newHarness(t)

	status, body := h.getJSON("/api/v1/nope")
	if status != http.StatusNotFound || body["error"] == nil {
		t.Fatalf("status = %d body = %v", status, body)
	}

	status, raw := h.do(http.MethodDelete, "/api/v1/businesses/biz_1", nil, nil)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", status)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out["error"] == nil {
		t.Fatalf("405 body was not a JSON error: %s", raw)
	}
}
