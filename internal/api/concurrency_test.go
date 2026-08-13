package api_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// The whole stack under concurrent HTTP load. Run with -race, this is what
// catches unsynchronised state in the store, the cache or a handler.
func TestConcurrentReviewRequestsKeepAggregatesExact(t *testing.T) {
	const writers = 40

	e := newE2E(t)
	// Warm the cache so every write has an entry to invalidate.
	if status, _, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil); status != http.StatusOK {
		t.Fatalf("warm-up status = %d", status)
	}

	statuses := make([]int, writers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			payload := fmt.Sprintf(`{"user_id":"user_%d","rating":5,"body":"concurrent"}`, i)
			statuses[i], _, _ = e.request(http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(payload), nil)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusCreated {
			t.Fatalf("request %d: status = %d, want 201", i, status)
		}
	}

	_, body, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1", nil, nil)
	wantCount := 3 + writers
	if count, _ := intOf(body["review_count"]); count != wantCount {
		t.Fatalf("review_count = %v, want %d", body["review_count"], wantCount)
	}
	want := float64(13+5*writers) / float64(wantCount)
	if avg, _ := body["average_rating"].(float64); avg != want {
		t.Fatalf("average_rating = %v, want %v", avg, want)
	}

	// Every review is retrievable: nothing was dropped by a concurrent write.
	_, page, _ := e.request(http.MethodGet, "/api/v1/businesses/biz_1/reviews?limit=100", nil, nil)
	data, _ := page["data"].([]any)
	if len(data) != wantCount {
		t.Fatalf("listing returned %d reviews, want %d", len(data), wantCount)
	}
	seen := map[string]bool{}
	for _, item := range data {
		review, _ := item.(map[string]any)
		id, _ := review["id"].(string)
		if seen[id] {
			t.Fatalf("review %s was returned twice", id)
		}
		seen[id] = true
	}
}

// Concurrent redelivery of one webhook over real HTTP: every delivery is
// acknowledged, and the subscription is written exactly once.
func TestConcurrentWebhookDeliveriesApplyOnce(t *testing.T) {
	const deliveries = 24

	e := newE2E(t)
	e.google.issue("tok-user", "google-sub-1", "user@example.com", "User")

	status, body, _ := e.request(http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-user"}`), nil)
	if status != http.StatusOK {
		t.Fatalf("sign-in status = %d", status)
	}
	user, _ := body["user"].(map[string]any)
	userID, _ := user["id"].(string)

	payload := fmt.Sprintf(`{"user_id":%q,"email":"user@example.com","plan_code":"PLN_x","amount":500000}`, userID)
	status, initialized, _ := e.request(http.MethodPost, "/api/v1/subscriptions/initialize", []byte(payload), nil)
	if status != http.StatusOK {
		t.Fatalf("initialize status = %d: %v", status, initialized)
	}
	reference, _ := initialized["reference"].(string)

	webhook := []byte(fmt.Sprintf(`{"event":"charge.success","data":{"id":7777,"reference":%q,"status":"success",`+
		`"customer":{"email":"user@example.com"},"plan":{"plan_code":"PLN_x"},"metadata":{"user_id":%q}}}`, reference, userID))
	signature := e2eSign(webhook)

	before, err := e.store.GetSubscription(userID)
	if err != nil {
		t.Fatal(err)
	}

	statuses := make([]int, deliveries)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i], _, _ = e.request(http.MethodPost, "/api/v1/subscriptions/webhook", webhook,
				map[string]string{"x-paystack-signature": signature})
		}(i)
	}
	close(start)
	wg.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200", i, status)
		}
	}

	after, err := e.store.GetSubscription(userID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "active" {
		t.Fatalf("status = %q, want active", after.Status)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Fatal("the event was never applied")
	}
	if after.LastEventID != "charge.success:7777" {
		t.Fatalf("last event = %q", after.LastEventID)
	}
}

// Mixed traffic across every endpoint at once: nothing may return 5xx.
func TestMixedConcurrentTrafficNeverReturns5xx(t *testing.T) {
	e := newE2E(t)
	e.google.issue("tok-mixed", "google-sub-mixed", "mixed@example.com", "Mixed")

	type call struct {
		method, path string
		body         []byte
	}
	calls := []call{
		{http.MethodGet, "/health", nil},
		{http.MethodGet, "/api/v1/businesses/biz_1", nil},
		{http.MethodGet, "/api/v1/businesses/biz_2", nil},
		{http.MethodGet, "/api/v1/businesses/biz_1/reviews?page=1&limit=2", nil},
		{http.MethodGet, "/api/v1/businesses/nope", nil},
		{http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(`{"user_id":"u","rating":4,"body":"ok"}`)},
		{http.MethodPost, "/api/v1/businesses/biz_1/reviews", []byte(`{"user_id":"u","rating":99}`)},
		{http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"tok-mixed"}`)},
		{http.MethodPost, "/api/v1/auth/google", []byte(`{"id_token":"nope"}`)},
		{http.MethodPost, "/api/v1/subscriptions/webhook", []byte(`{"event":"charge.success","data":{"id":1}}`)},
	}

	const rounds = 6
	results := make([]int, len(calls)*rounds)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for round := 0; round < rounds; round++ {
		for i, c := range calls {
			wg.Add(1)
			go func(slot int, c call) {
				defer wg.Done()
				<-start
				results[slot], _, _ = e.request(c.method, c.path, c.body, nil)
			}(round*len(calls)+i, c)
		}
	}
	close(start)
	wg.Wait()

	for slot, status := range results {
		if status >= http.StatusInternalServerError {
			t.Fatalf("%s %s returned %d", calls[slot%len(calls)].method, calls[slot%len(calls)].path, status)
		}
		if status == 0 {
			t.Fatalf("request %d never completed", slot)
		}
	}
}
