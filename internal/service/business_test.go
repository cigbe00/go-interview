package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

// failingCache simulates a Redis outage on every operation.
type failingCache struct {
	err     error
	deletes int
}

func (f *failingCache) GetBusiness(context.Context, string) (model.Business, bool, error) {
	return model.Business{}, false, f.err
}
func (f *failingCache) SetBusiness(context.Context, model.Business, time.Duration) error {
	return f.err
}
func (f *failingCache) DeleteBusiness(context.Context, string) error {
	f.deletes++
	return f.err
}

func newBusinessService(t *testing.T, c cache.BusinessCache) (*service.BusinessService, *store.MemoryStore) {
	t.Helper()
	st := store.NewMemoryStore()
	return &service.BusinessService{Store: st, Cache: c, CacheTTL: time.Minute}, st
}

// Symptom 2, end to end through the service: creating a review must be
// reflected in the business aggregates.
func TestCreateReviewUpdatesBusinessAggregates(t *testing.T) {
	svc, _ := newBusinessService(t, cache.NewMemoryBusinessCache())
	ctx := context.Background()

	before, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if before.ReviewCount != 3 {
		t.Fatalf("seeded review count = %d, want 3", before.ReviewCount)
	}

	if _, err := svc.CreateReview(ctx, "biz_1", "user_9", 1, "Not great"); err != nil {
		t.Fatal(err)
	}

	after, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ReviewCount != 4 {
		t.Fatalf("review count = %d, want 4", after.ReviewCount)
	}
	const want = 14.0 / 4.0 // 5+4+4+1
	if diff := after.Average - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("average = %v, want %v", after.Average, want)
	}
}

// Symptom 5: the cached business embeds review_count/average_rating, so a new
// review has to invalidate it. Without invalidation this returns the value
// cached by the first read.
func TestCreateReviewInvalidatesCachedBusiness(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	svc, _ := newBusinessService(t, c)
	ctx := context.Background()

	if _, err := svc.GetBusiness(ctx, "biz_1"); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 1 {
		t.Fatalf("expected the first read to populate the cache, have %d entries", c.Len())
	}

	if _, err := svc.CreateReview(ctx, "biz_1", "user_9", 1, "Not great"); err != nil {
		t.Fatal(err)
	}
	if c.Len() != 0 {
		t.Fatal("cached business survived a write that changed its derived data")
	}

	after, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ReviewCount != 4 {
		t.Fatalf("served stale cached business: review count = %d, want 4", after.ReviewCount)
	}
}

func TestGetBusinessServesAndRepopulatesCache(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	svc, _ := newBusinessService(t, c)
	ctx := context.Background()

	// A primed entry is served as-is rather than re-read from the store.
	primed := model.Business{ID: "biz_1", Name: "Cached Name", ReviewCount: 99, Average: 1.5}
	if err := c.SetBusiness(ctx, primed, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Cached Name" || got.ReviewCount != 99 {
		t.Fatalf("cache was not consulted: %+v", got)
	}

	if err := c.DeleteBusiness(ctx, "biz_1"); err != nil {
		t.Fatal(err)
	}
	if got, err = svc.GetBusiness(ctx, "biz_1"); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lagos Bistro" || got.ReviewCount != 3 {
		t.Fatalf("store read after miss = %+v", got)
	}
	if c.Len() != 1 {
		t.Fatal("a miss should repopulate the cache")
	}
}

// A transient cache failure must degrade to the store, never turn into a
// failed request or a lost write.
func TestCacheOutageDoesNotCorruptOrFailRequests(t *testing.T) {
	fc := &failingCache{err: errors.New("redis: connection refused")}
	svc, st := newBusinessService(t, fc)
	ctx := context.Background()

	b, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatalf("read failed during a cache outage: %v", err)
	}
	if b.ReviewCount != 3 {
		t.Fatalf("unexpected %+v", b)
	}

	r, err := svc.CreateReview(ctx, "biz_1", "user_9", 5, "Great")
	if err != nil {
		t.Fatalf("write failed during a cache outage: %v", err)
	}
	if r.ID == "" {
		t.Fatal("expected a persisted review")
	}
	if fc.deletes != 1 {
		t.Fatalf("invalidation attempts = %d, want 1", fc.deletes)
	}
	// The write itself must still be durable.
	if count, _ := st.ReviewStats("biz_1"); count != 4 {
		t.Fatalf("review count in store = %d, want 4", count)
	}
}

// Invalidation must not be abandoned because the caller went away after the
// write committed.
func TestInvalidationRunsAfterClientDisconnect(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	svc, _ := newBusinessService(t, c)

	if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := svc.CreateReview(ctx, "biz_1", "user_9", 5, "Great"); err != nil {
		t.Fatal(err)
	}
	cancel()
	if c.Len() != 0 {
		t.Fatal("cache entry survived a committed write")
	}
}

func TestCreateReviewValidation(t *testing.T) {
	svc, _ := newBusinessService(t, cache.NewMemoryBusinessCache())
	ctx := context.Background()

	cases := []struct {
		name       string
		businessID string
		userID     string
		rating     int
		body       string
		want       error
	}{
		{"rating too low", "biz_1", "user_9", 0, "x", service.ErrInvalidRating},
		{"rating too high", "biz_1", "user_9", 6, "x", service.ErrInvalidRating},
		{"missing user", "biz_1", "   ", 5, "x", service.ErrUserRequired},
		{"unknown business", "nope", "user_9", 5, "x", store.ErrNotFound},
		{"oversized body", "biz_1", "user_9", 5, string(make([]byte, 5001)), service.ErrBodyTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateReview(ctx, tc.businessID, tc.userID, tc.rating, tc.body)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A rejected review must never reach the store.
func TestInvalidReviewIsNotPersisted(t *testing.T) {
	svc, st := newBusinessService(t, cache.NewMemoryBusinessCache())
	if _, err := svc.CreateReview(context.Background(), "biz_1", "user_9", 9, "x"); err == nil {
		t.Fatal("expected a validation error")
	}
	if count, _ := st.ReviewStats("biz_1"); count != 3 {
		t.Fatalf("review count = %d, want 3", count)
	}
}

func TestListReviewsRejectsUnknownBusiness(t *testing.T) {
	svc, _ := newBusinessService(t, cache.NewMemoryBusinessCache())
	if _, _, err := svc.ListReviews(context.Background(), "nope", 1, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListReviewsClampsPagingArguments(t *testing.T) {
	svc, _ := newBusinessService(t, cache.NewMemoryBusinessCache())
	ctx := context.Background()

	reviews, total, err := svc.ListReviews(ctx, "biz_1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(reviews) != 3 {
		t.Fatalf("got %d of %d, want 3 of 3", len(reviews), total)
	}
	if reviews[0].ID != "rev_3" {
		t.Fatalf("first review = %s, want the newest (rev_3)", reviews[0].ID)
	}
}
