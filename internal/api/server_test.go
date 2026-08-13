package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
)

// Handler-level tests. They share the harness in e2e_test.go rather than
// standing up a second one; the end-to-end journeys there cover the happy
// paths, so these concentrate on status codes, validation and framing.

func TestHealth(t *testing.T) {
	e := newE2EWithRedis(t, true)
	status, body, _ := e.request(http.MethodGet, "/health", nil, nil)
	if status != http.StatusOK || body["status"] != "ok" || body["redis"] != true {
		t.Fatalf("status=%d body=%v", status, body)
	}
}

func TestGetBusinessByDocumentedID(t *testing.T) {
	e := newE2E(t)

	status, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["id"] != "biz_1" || body["name"] != "Lagos Bistro" {
		t.Fatalf("unexpected body %v", body)
	}
	if count, _ := intOf(body["review_count"]); count != 3 {
		t.Fatalf("review_count = %v, want 3", body["review_count"])
	}
	// 5+4+4 over three reviews: the average must not be truncated to 4.
	if avg, _ := body["average_rating"].(float64); avg < 4.33 || avg > 4.34 {
		t.Fatalf("average_rating = %v, want ~4.333", body["average_rating"])
	}

	if status, _, _ := e.request(http.MethodGet, "/api/v1/businesses/nope", nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown business status = %d, want 404", status)
	}
}

// The full reported symptom, over HTTP: create a review, then read the
// business back and see the new aggregates rather than a cached copy.
func TestCreateReviewIsReflectedInBusinessImmediately(t *testing.T) {
	e := newE2E(t)

	if status, _, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil); status != http.StatusOK {
		t.Fatalf("warm-up read status = %d", status)
	}

	status, created, _ := e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews",
		[]byte(`{"user_id":"user_99","rating":1,"body":"Not great"}`), nil)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", status, created)
	}
	if created["id"] == "" || created["business_id"] != "biz_1" {
		t.Fatalf("unexpected review %v", created)
	}

	_, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
	if count, _ := intOf(body["review_count"]); count != 4 {
		t.Fatalf("review_count = %v, want 4 (stale cache?)", body["review_count"])
	}
	if avg, _ := body["average_rating"].(float64); avg != 3.5 {
		t.Fatalf("average_rating = %v, want 3.5", body["average_rating"])
	}
}

func TestListReviewsPagination(t *testing.T) {
	e := newE2E(t)

	status, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews?page=1&limit=2", nil, nil)
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
	if total, _ := intOf(body["total"]); total != 3 {
		t.Fatalf("total = %v, want 3", body["total"])
	}

	_, page2, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews?page=2&limit=2", nil, nil)
	data2, _ := page2["data"].([]any)
	if len(data2) != 1 {
		t.Fatalf("page 2 returned %d reviews, want 1", len(data2))
	}

	// Defaults apply when the parameters are omitted.
	_, defaults, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews", nil, nil)
	if len(defaults["data"].([]any)) != 3 {
		t.Fatalf("default page = %v", defaults)
	}
}

func TestListReviewsRequestValidation(t *testing.T) {
	e := newE2E(t)
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
			if status, _, _ := e.request(http.MethodGet, tc.path, nil, nil); status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
		})
	}
}

func TestCreateReviewRequestValidation(t *testing.T) {
	e := newE2E(t)
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
			status, body, _ := e.request(http.MethodPost, tc.path, []byte(tc.body), nil)
			if status != tc.want {
				t.Fatalf("status = %d, want %d (%v)", status, tc.want, body)
			}
			if body["error"] == nil {
				t.Fatalf("error responses must carry an error field: %v", body)
			}
		})
	}
}

// The status mapping matters more than the message: a provider outage must
// never be reported to a legitimate user as a rejected credential.
func TestGoogleAuthStatusMapping(t *testing.T) {
	e := newE2E(t)
	e.google.issue("tok", "google-1", "user@example.com", "Test User")

	status, body, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok"}`), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%v)", status, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["google_id"] != "google-1" || user["email"] != "user@example.com" {
		t.Fatalf("unexpected user %v", body)
	}

	// A rejected credential is the caller's problem: 401.
	e.auth.Verifier = auth.FakeVerifier{Err: auth.ErrInvalidToken}
	if status, _, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok"}`), nil); status != http.StatusUnauthorized {
		t.Fatalf("invalid token status = %d, want 401", status)
	}

	// A provider outage is ours: 502, not a false "your login is invalid".
	e.auth.Verifier = auth.FakeVerifier{Err: auth.ErrProviderUnavailable}
	if status, _, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok"}`), nil); status != http.StatusBadGateway {
		t.Fatalf("provider outage status = %d, want 502", status)
	}
}

func TestSubscriptionInitializeValidation(t *testing.T) {
	e := newE2E(t)

	// Bad input is rejected before the provider is called.
	if status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/initialize",
		[]byte(`{"email":"user@example.com","amount":5000}`), nil); status != http.StatusBadRequest {
		t.Fatalf("missing user_id status = %d, want 400", status)
	}

	// A provider failure is an upstream problem, not a client error.
	e.subs.Provider = payments.FakeProvider{InitErr: payments.ErrProvider}
	if status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/initialize",
		[]byte(`{"user_id":"user_1","email":"user@example.com","amount":5000}`), nil); status != http.StatusBadGateway {
		t.Fatalf("provider failure status = %d, want 502", status)
	}

	if status, _, _ := e.request(http.MethodGet, "/api/v1/subscriptions/nobody", nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown subscription status = %d, want 404", status)
	}
}

func chargeSuccessPayload(reference, padding string) []byte {
	return chargeSuccessPayloadFor(reference, "user_1", padding)
}

func chargeSuccessPayloadFor(reference, userID, padding string) []byte {
	return []byte(`{"event":"charge.success","data":{"id":42,"reference":"` + reference +
		`","status":"success","customer":{"email":"user@example.com"},"plan":{"plan_code":"PLN_x"},` +
		`"metadata":{"user_id":"` + userID + `"},"log":"` + padding + `"}}`)
}

// The signature covers the exact bytes received, so the handler has to read the
// body in full. Reading only what Content-Length advertised — or a single
// Read call — truncated large and chunked payloads, which then failed
// verification even though the delivery was genuine.
func TestWebhookReadsWholeBodyRegardlessOfFraming(t *testing.T) {
	t.Run("body spanning multiple reads", func(t *testing.T) {
		e := newE2E(t)
		reference := e.seedPendingSubscription(t, "user_1")

		body := chargeSuccessPayload(reference, strings.Repeat("x", 64*1024))
		status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", body,
			map[string]string{"x-paystack-signature": e2eSign(body)})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if sub, _ := e.store.GetSubscription("user_1"); sub.Status != "active" {
			t.Fatalf("subscription = %+v, want active", sub)
		}
	})

	t.Run("chunked body with no content-length", func(t *testing.T) {
		e := newE2E(t)
		reference := e.seedPendingSubscription(t, "user_1")

		body := chargeSuccessPayload(reference, "")
		// io.NopCloser hides the concrete reader type, so net/http cannot
		// derive a Content-Length and sends the request chunked.
		status, _, _ := e.requestReader(http.MethodPost, "/api/v1/subscriptions/webhook",
			io.NopCloser(bytes.NewReader(body)),
			map[string]string{"Content-Type": "application/json", "x-paystack-signature": e2eSign(body)})
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if sub, _ := e.store.GetSubscription("user_1"); sub.Status != "active" {
			t.Fatalf("subscription = %+v, want active", sub)
		}
	})
}

func TestWebhookRejectsUnsignedAndUnknownDeliveries(t *testing.T) {
	e := newE2E(t)
	reference := e.seedPendingSubscription(t, "user_1")
	body := chargeSuccessPayload(reference, "")

	cases := []struct {
		name      string
		body      []byte
		signature string
		want      int
	}{
		{"missing signature", body, "", http.StatusUnauthorized},
		{"wrong signature", body, e2eSign([]byte("something else")), http.StatusUnauthorized},
		{"signature over a different body", chargeSuccessPayload(reference, "tampered"), e2eSign(body), http.StatusUnauthorized},
		{
			"unknown subscription",
			chargeSuccessPayloadFor("ref_unknown", "user_nobody", ""),
			e2eSign(chargeSuccessPayloadFor("ref_unknown", "user_nobody", "")),
			http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", tc.body,
				map[string]string{"x-paystack-signature": tc.signature})
			if status != tc.want {
				t.Fatalf("status = %d, want %d", status, tc.want)
			}
			if sub, _ := e.store.GetSubscription("user_1"); sub.Status != "pending" {
				t.Fatalf("rejected delivery changed state: %+v", sub)
			}
		})
	}
}

// An event we do not act on is acknowledged so the provider stops retrying it.
func TestWebhookAcknowledgesUnsupportedEvent(t *testing.T) {
	e := newE2E(t)
	e.seedPendingSubscription(t, "user_1")

	body := []byte(`{"event":"customeridentification.failed","data":{"id":7}}`)
	status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/webhook", body,
		map[string]string{"x-paystack-signature": e2eSign(body)})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if sub, _ := e.store.GetSubscription("user_1"); sub.Status != "pending" {
		t.Fatalf("unsupported event changed state: %+v", sub)
	}
}

// Framework-generated errors must use the same JSON envelope as handlers.
func TestUnroutedRequestsReturnJSONErrors(t *testing.T) {
	e := newE2E(t)

	status, body, _ := e.request(http.MethodGet, "/api/v1/nope", nil, nil)
	if status != http.StatusNotFound || body["error"] == nil {
		t.Fatalf("status = %d body = %v", status, body)
	}

	status, decoded, _ := e.request(http.MethodDelete, "/api/v1/businesses/biz_1", nil, nil)
	if status != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", status)
	}
	if decoded["error"] == nil {
		t.Fatalf("405 body was not a JSON error: %v", decoded)
	}
	// Guard the envelope shape itself: one key, always a string.
	raw, _ := json.Marshal(decoded)
	if !strings.HasPrefix(string(raw), `{"error":"`) {
		t.Fatalf("unexpected error envelope: %s", raw)
	}
}
