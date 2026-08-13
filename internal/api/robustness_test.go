package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

// An oversized body must be refused rather than buffered into memory.
func TestOversizedBodyIsRejected(t *testing.T) {
	e := newE2E(t)
	huge := fmt.Sprintf(`{"user_id":"u","rating":5,"body":%q}`, strings.Repeat("x", 2<<20))

	status, _, _ := e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(huge), nil)
	if status != http.StatusRequestEntityTooLarge && status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 413 (or 400); the body limit is not enforced", status)
	}

	// The oversized request must not have been persisted, and the service must
	// still be serving.
	_, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
	if count, _ := intOf(body["review_count"]); count != 3 {
		t.Fatalf("review_count = %v, want 3", body["review_count"])
	}
}

// A body just under the limit but over the field cap is a validation error,
// not a transport error.
func TestOversizedReviewBodyIsAValidationError(t *testing.T) {
	e := newE2E(t)
	payload := fmt.Sprintf(`{"user_id":"u","rating":5,"body":%q}`, strings.Repeat("x", 6000))

	status, body, _ := e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(payload), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if body["error"] == nil {
		t.Fatalf("no error message: %v", body)
	}
}

func TestMalformedRequestsAreRejectedCleanly(t *testing.T) {
	e := newE2E(t)
	cases := []struct {
		name, method, path, body string
		contentType              string
		wantMin, wantMax         int
	}{
		{"empty body", http.MethodPost, "/api/v1/businesses/biz_1/reviews", "", "application/json", 400, 400},
		{"truncated json", http.MethodPost, "/api/v1/businesses/biz_1/reviews", `{"user_id":"u"`, "application/json", 400, 400},
		{"json array instead of object", http.MethodPost, "/api/v1/businesses/biz_1/reviews", `[]`, "application/json", 400, 400},
		{"wrong content type", http.MethodPost, "/api/v1/businesses/biz_1/reviews", `user_id=u&rating=5`, "text/plain", 400, 415},
		{"null body", http.MethodPost, "/api/v1/auth/google", `null`, "application/json", 400, 401},
		{"deeply nested json", http.MethodPost, "/api/v1/auth/google", strings.Repeat(`{"a":`, 200) + `1` + strings.Repeat(`}`, 200), "application/json", 400, 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _ := e.request(tc.method, tc.path, []byte(tc.body),
				map[string]string{"Content-Type": tc.contentType})
			if status < tc.wantMin || status > tc.wantMax {
				t.Fatalf("status = %d, want between %d and %d", status, tc.wantMin, tc.wantMax)
			}
			if status >= http.StatusInternalServerError {
				t.Fatalf("malformed input produced a server error: %d", status)
			}
		})
	}
}

// Unicode and control characters must survive a round trip intact.
func TestReviewBodyHandlesUnicode(t *testing.T) {
	e := newE2E(t)
	const body = "Great jollof 🍚 — ₦5,000 well spent. \"Best\" in Lagos.\nNewline too."

	payload, err := jsonPayload(map[string]any{"user_id": "user_1", "rating": 5, "body": body})
	if err != nil {
		t.Fatal(err)
	}
	status, created, _ := e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews", payload, nil)
	if status != http.StatusCreated {
		t.Fatalf("status = %d: %v", status, created)
	}
	if created["body"] != body {
		t.Fatalf("body round trip changed the text:\n got %q\nwant %q", created["body"], body)
	}

	_, page, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews?limit=100", nil, nil)
	data, _ := page["data"].([]any)
	found := false
	for _, item := range data {
		review, _ := item.(map[string]any)
		if review["body"] == body {
			found = true
		}
	}
	if !found {
		t.Fatal("the unicode review was not returned by the listing")
	}
}

// Pagination must be exhaustive and non-overlapping for every page size, not
// just the one the happy-path test uses.
func TestPaginationIsExhaustiveForEveryPageSize(t *testing.T) {
	e := newE2E(t)
	for i := 0; i < 9; i++ {
		payload := fmt.Sprintf(`{"user_id":"user_%d","rating":%d,"body":"review %d"}`, i, 1+i%5, i)
		if status, _, _ := e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(payload), nil); status != http.StatusCreated {
			t.Fatalf("seed review %d: status = %d", i, status)
		}
	}

	// Establish the full set once.
	_, all, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews?limit=100", nil, nil)
	everything, _ := all["data"].([]any)
	expected := map[string]bool{}
	for _, item := range everything {
		review, _ := item.(map[string]any)
		id, _ := review["id"].(string)
		expected[id] = true
	}
	if len(expected) != 12 { // 3 seeded + 9 created
		t.Fatalf("expected 12 reviews, found %d", len(expected))
	}

	for limit := 1; limit <= 7; limit++ {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			seen := map[string]int{}
			for page := 1; page <= 30; page++ {
				_, body, _ := e.request(http.MethodGet,
					fmt.Sprintf("/api/v1/businesses/biz_1/reviews?page=%d&limit=%d", page, limit), nil, nil)
				data, _ := body["data"].([]any)
				if len(data) == 0 {
					break
				}
				if len(data) > limit {
					t.Fatalf("page %d returned %d reviews, more than the limit of %d", page, len(data), limit)
				}
				for _, item := range data {
					review, _ := item.(map[string]any)
					id, _ := review["id"].(string)
					seen[id]++
				}
			}
			if len(seen) != len(expected) {
				t.Fatalf("paging with limit %d covered %d reviews, want %d", limit, len(seen), len(expected))
			}
			for id, n := range seen {
				if n != 1 {
					t.Fatalf("limit %d: review %s appeared %d times", limit, id, n)
				}
				if !expected[id] {
					t.Fatalf("limit %d: paging returned unknown review %s", limit, id)
				}
			}
		})
	}
}

// alwaysFailingCache stands in for a Redis outage at the HTTP boundary.
type alwaysFailingCache struct{ err error }

func (c alwaysFailingCache) GetBusiness(context.Context, string) (model.Business, bool, error) {
	return model.Business{}, false, c.err
}
func (c alwaysFailingCache) SetBusiness(context.Context, model.Business, time.Duration) error {
	return c.err
}
func (c alwaysFailingCache) DeleteBusiness(context.Context, string) error { return c.err }

// With Redis down, the API must keep serving reads and accepting writes.
func TestApiKeepsServingWhileCacheIsDown(t *testing.T) {
	st := store.NewMemoryStore()
	srv := api.New(
		&service.BusinessService{
			Store: st,
			Cache: alwaysFailingCache{err: fmt.Errorf("dial tcp 127.0.0.1:6379: connect: connection refused")},
		},
		&service.AuthService{Store: st, Verifier: auth.FakeVerifier{Identity: auth.Identity{Subject: "s", Email: "e@x.co"}}},
		&service.SubscriptionService{Store: st, Provider: payments.FakeProvider{}},
		func() bool { return false },
	)
	ts := httptest.NewServer(srv.Echo)
	defer ts.Close()

	e := &e2eEnv{t: t, client: ts.Client(), baseURL: ts.URL, store: st}

	// Health reports the outage without failing.
	status, health, _ := e.request(http.MethodGet, "/health", nil, nil)
	if status != http.StatusOK || health["redis"] != false {
		t.Fatalf("status=%d health=%v", status, health)
	}

	// Reads are served from the store.
	status, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("read status = %d, want 200", status)
	}
	if count, _ := intOf(body["review_count"]); count != 3 {
		t.Fatalf("review_count = %v", body["review_count"])
	}

	// Writes still succeed, and remain visible, even though invalidation fails.
	status, _, _ = e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews",
		[]byte(`{"user_id":"u","rating":1,"body":"Not great"}`), nil)
	if status != http.StatusCreated {
		t.Fatalf("write status = %d, want 201", status)
	}
	_, after, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
	if count, _ := intOf(after["review_count"]); count != 4 {
		t.Fatalf("review_count = %v, want 4", after["review_count"])
	}
}

// A hanging payment provider must surface as a bounded 502, not a hung
// request that ties up a connection until the server write timeout.
func TestSlowProviderFailsFastAsBadGateway(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		slow.Close()
	}()

	st := store.NewMemoryStore()
	srv := api.New(
		&service.BusinessService{Store: st},
		&service.AuthService{Store: st, Verifier: auth.FakeVerifier{}},
		&service.SubscriptionService{Store: st, Provider: &payments.PaystackClient{
			SecretKey:  "sk_test_slow",
			BaseURL:    slow.URL,
			HTTPClient: &http.Client{Timeout: 300 * time.Millisecond},
		}},
		func() bool { return false },
	)
	ts := httptest.NewServer(srv.Echo)
	defer ts.Close()
	e := &e2eEnv{t: t, client: ts.Client(), baseURL: ts.URL, store: st}

	start := time.Now()
	status, _, _ := e.request(http.MethodPost, "/api/v1/subscriptions/initialize",
		[]byte(`{"user_id":"u1","email":"u@example.com","plan_code":"PLN_x","amount":5000}`), nil)
	elapsed := time.Since(start)

	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("request took %s; the provider timeout is not bounding it", elapsed)
	}
	// A failed checkout must not leave a subscription behind.
	if _, err := st.GetSubscription("u1"); err == nil {
		t.Fatal("a failed initialization persisted a subscription")
	}
}

// brokenRepo fails every persistence call, the way a database outage does.
type brokenRepo struct{ err error }

func (b brokenRepo) GetBusiness(string) (model.Business, error) {
	return model.Business{}, b.err
}
func (b brokenRepo) SaveReview(model.Review) error                      { return b.err }
func (b brokenRepo) ListReviews(string, int, int) ([]model.Review, int) { return nil, 0 }
func (b brokenRepo) ReviewStats(string) (int, float64)                  { return 0, 0 }
func (b brokenRepo) GetSubscription(string) (model.Subscription, error) {
	return model.Subscription{}, b.err
}
func (b brokenRepo) GetSubscriptionByReference(string) (model.Subscription, error) {
	return model.Subscription{}, b.err
}
func (b brokenRepo) PutSubscription(model.Subscription) {}
func (b brokenRepo) MarkEventProcessed(string) bool     { return true }

// A datastore outage must surface as 500 with a fixed message — not a 404
// (which would say the data does not exist), and not the driver's error text.
func TestDatastoreOutageReturns500WithoutLeakingDetail(t *testing.T) {
	const driverDetail = "server selection timeout: no reachable servers rs0/mongo-1:27017"
	repo := brokenRepo{err: errors.New(driverDetail)}

	srv := api.New(
		&service.BusinessService{Store: repo},
		&service.AuthService{Store: store.NewMemoryStore(), Verifier: auth.FakeVerifier{}},
		&service.SubscriptionService{Store: repo, Provider: payments.FakeProvider{}},
		func() bool { return false },
	)
	ts := httptest.NewServer(srv.Echo)
	defer ts.Close()
	e := &e2eEnv{t: t, client: ts.Client(), baseURL: ts.URL}

	cases := []struct {
		name, method, path, body string
	}{
		{"read business", http.MethodGet, "/api/v1/businesses/biz_1", ""},
		{"list reviews", http.MethodGet, "/api/v1/businesses/biz_1/reviews", ""},
		{"create review", http.MethodPost, "/api/v1/businesses/biz_1/reviews", `{"user_id":"u","rating":5,"body":"x"}`},
		{"read subscription", http.MethodGet, "/api/v1/subscriptions/usr_1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload []byte
			if tc.body != "" {
				payload = []byte(tc.body)
			}
			status, body, _ := e.request(tc.method, tc.path, payload, nil)
			if status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", status)
			}
			message, _ := body["error"].(string)
			if message == "" {
				t.Fatalf("no error message: %v", body)
			}
			if strings.Contains(message, "mongo") || strings.Contains(message, driverDetail) {
				t.Fatalf("internal detail leaked to the client: %q", message)
			}
		})
	}
}
