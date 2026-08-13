package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

// Paystack can deliver the same webhook more than once, and those deliveries
// can overlap. Sequential deduplication is not enough: the claim has to be
// atomic, or two in-flight copies of one event both apply.
func TestConcurrentRedeliveryAppliesExactlyOnce(t *testing.T) {
	const deliveries = 32

	repo := &stubSubscriptionRepo{
		sub:   model.Subscription{UserID: "usr_1", Status: "pending", Reference: "ref_1", PlanCode: "PLN_x"},
		byRef: map[string]model.Subscription{"ref_1": {UserID: "usr_1", Status: "pending", Reference: "ref_1", PlanCode: "PLN_x"}},
	}
	svc := &service.SubscriptionService{
		Store: repo,
		Provider: payments.FakeProvider{Event: payments.WebhookEvent{
			ID: "charge.success:9001", Type: "charge.success", Reference: "ref_1",
			PlanCode: "PLN_x", Status: "active",
		}},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, deliveries)
	for i := 0; i < deliveries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together to maximise overlap
			errs[i] = svc.HandleWebhook(context.Background(), []byte(`{"event":"charge.success"}`), "sig")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("delivery %d failed: %v", i, err)
		}
	}
	if got := repo.putCount(); got != 1 {
		t.Fatalf("the same event was applied %d times, want exactly 1", got)
	}
}

// Distinct events must not be lost to the deduplication that protects against
// duplicates — every one of them has to be applied.
func TestConcurrentDistinctEventsAreAllApplied(t *testing.T) {
	const events = 16

	repo := &stubSubscriptionRepo{
		sub:   model.Subscription{UserID: "usr_1", Status: "pending", Reference: "ref_1"},
		byRef: map[string]model.Subscription{"ref_1": {UserID: "usr_1", Status: "pending", Reference: "ref_1"}},
	}

	var wg sync.WaitGroup
	for i := 0; i < events; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := &service.SubscriptionService{
				Store: repo,
				Provider: payments.FakeProvider{Event: payments.WebhookEvent{
					ID: "charge.success:" + time.Duration(i).String(), Type: "charge.success",
					Reference: "ref_1", Status: "active",
				}},
			}
			if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err != nil {
				t.Errorf("event %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if got := repo.putCount(); got != events {
		t.Fatalf("%d of %d distinct events were applied", got, events)
	}
}

// Concurrent review writes must not lose updates or corrupt the aggregates.
func TestConcurrentReviewWritesKeepAggregatesExact(t *testing.T) {
	const writers = 40

	st := store.NewMemoryStore()
	c := cache.NewMemoryBusinessCache()
	svc := &service.BusinessService{Store: st, Cache: c, CacheTTL: time.Minute}
	ctx := context.Background()

	// Warm the cache so every write has an entry to invalidate.
	if _, err := svc.GetBusiness(ctx, "biz_1"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.CreateReview(ctx, "biz_1", "user_c", 5, "Great"); err != nil {
				t.Errorf("concurrent write failed: %v", err)
			}
		}()
	}
	wg.Wait()

	// No reader ran during the writes, so no entry can have been repopulated:
	// the next read is served from the store and must be exact.
	b, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	wantCount := 3 + writers
	wantAvg := float64(13+5*writers) / float64(wantCount)
	if b.ReviewCount != wantCount {
		t.Fatalf("review_count = %d, want %d", b.ReviewCount, wantCount)
	}
	if b.Average != wantAvg {
		t.Fatalf("average = %v, want %v", b.Average, wantAvg)
	}
}

// Readers and writers running together must never fail, and the store must
// stay exact regardless of what the cache does.
func TestConcurrentReadsAndWritesStayConsistent(t *testing.T) {
	const writers, readers = 25, 25

	st := store.NewMemoryStore()
	c := cache.NewMemoryBusinessCache()
	svc := &service.BusinessService{Store: st, Cache: c, CacheTTL: time.Minute}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.CreateReview(ctx, "biz_1", "user_c", 4, "Good"); err != nil {
				t.Errorf("write failed: %v", err)
			}
		}()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.GetBusiness(ctx, "biz_1"); err != nil {
				t.Errorf("read failed: %v", err)
			}
			if _, _, err := svc.ListReviews(ctx, "biz_1", 1, 10); err != nil {
				t.Errorf("list failed: %v", err)
			}
		}()
	}
	wg.Wait()

	// The store is the source of truth and must be exact.
	count, avg := st.ReviewStats("biz_1")
	wantCount := 3 + writers
	if count != wantCount {
		t.Fatalf("store review count = %d, want %d", count, wantCount)
	}
	if want := float64(13+4*writers) / float64(wantCount); avg != want {
		t.Fatalf("store average = %v, want %v", avg, want)
	}

	// A reader that loaded from the store just before a write can repopulate
	// the cache after that write's invalidation, so the cached copy may lag by
	// design (documented in PULL_REQUEST.md). One more write clears it, and
	// with no readers racing, the next read must agree with the store exactly.
	if _, err := svc.CreateReview(ctx, "biz_1", "user_c", 4, "Good"); err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ReviewCount != wantCount+1 {
		t.Fatalf("read-back review_count = %d, want %d", b.ReviewCount, wantCount+1)
	}
}
