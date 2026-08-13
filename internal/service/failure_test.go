package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

// stubBusinessRepo injects persistence failures that the in-memory store
// cannot produce but MongoDB will (timeouts, write concern errors, dropped
// connections).
type stubBusinessRepo struct {
	business model.Business
	getErr   error
	saveErr  error
}

func (s *stubBusinessRepo) GetBusiness(string) (model.Business, error) {
	return s.business, s.getErr
}
func (s *stubBusinessRepo) SaveReview(model.Review) error { return s.saveErr }
func (s *stubBusinessRepo) ListReviews(string, int, int) ([]model.Review, int) {
	return []model.Review{}, 0
}
func (s *stubBusinessRepo) ReviewStats(string) (int, float64) { return 0, 0 }

// spyCache records what the service asks the cache to do, and can be told to
// fail every operation so a Redis outage can be simulated.
type spyCache struct {
	entries map[string]model.Business
	ttls    []time.Duration
	deletes int
	err     error
}

func newSpyCache() *spyCache { return &spyCache{entries: map[string]model.Business{}} }

func newFailingCache(err error) *spyCache {
	c := newSpyCache()
	c.err = err
	return c
}

func (c *spyCache) GetBusiness(_ context.Context, id string) (model.Business, bool, error) {
	if c.err != nil {
		return model.Business{}, false, c.err
	}
	b, ok := c.entries[id]
	return b, ok, nil
}
func (c *spyCache) SetBusiness(_ context.Context, b model.Business, ttl time.Duration) error {
	c.ttls = append(c.ttls, ttl)
	if c.err != nil {
		return c.err
	}
	c.entries[b.ID] = b
	return nil
}
func (c *spyCache) DeleteBusiness(_ context.Context, id string) error {
	c.deletes++
	if c.err != nil {
		return c.err
	}
	delete(c.entries, id)
	return nil
}

// A failed write must surface, not be swallowed into a 201.
func TestCreateReviewSurfacesStoreFailure(t *testing.T) {
	writeErr := errors.New("write concern error: not enough replicas")
	repo := &stubBusinessRepo{business: model.Business{ID: "biz_1"}, saveErr: writeErr}
	c := newSpyCache()
	svc := &service.BusinessService{Store: repo, Cache: c, CacheTTL: time.Minute}

	_, err := svc.CreateReview(context.Background(), "biz_1", "user_9", 5, "Great")
	if !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, want the store error", err)
	}
	// Nothing was written, so the cached copy is still correct. Invalidating
	// here would turn a failed write into pointless load on the store.
	if c.deletes != 0 {
		t.Fatalf("cache was invalidated for a write that never happened (%d deletes)", c.deletes)
	}
}

// A store error that is not "not found" must not be reported as a missing
// business — that would turn an outage into a silent 404.
func TestGetBusinessSurfacesStoreFailure(t *testing.T) {
	readErr := errors.New("server selection timeout")
	svc := &service.BusinessService{Store: &stubBusinessRepo{getErr: readErr}, Cache: newSpyCache()}

	_, err := svc.GetBusiness(context.Background(), "biz_1")
	if !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want the store error", err)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Fatal("a store outage was reported as ErrNotFound")
	}

	if _, _, err := svc.ListReviews(context.Background(), "biz_1", 1, 10); !errors.Is(err, readErr) {
		t.Fatalf("ListReviews err = %v, want the store error", err)
	}
}

// go-redis treats a zero expiration as "no expiry". If an unset CacheTTL
// reached Redis, cached businesses would live forever and the TTL backstop
// that bounds a failed invalidation would silently not exist.
func TestCacheTTLIsNeverZero(t *testing.T) {
	repo := &stubBusinessRepo{business: model.Business{ID: "biz_1"}}

	t.Run("unset TTL falls back to a positive default", func(t *testing.T) {
		c := newSpyCache()
		svc := &service.BusinessService{Store: repo, Cache: c} // CacheTTL left at zero
		if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
			t.Fatal(err)
		}
		if len(c.ttls) != 1 {
			t.Fatalf("expected exactly one cache write, got %d", len(c.ttls))
		}
		if c.ttls[0] <= 0 {
			t.Fatalf("cache TTL = %v; a non-positive TTL means the entry never expires", c.ttls[0])
		}
	})

	t.Run("negative TTL falls back too", func(t *testing.T) {
		c := newSpyCache()
		svc := &service.BusinessService{Store: repo, Cache: c, CacheTTL: -time.Second}
		if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
			t.Fatal(err)
		}
		if c.ttls[0] <= 0 {
			t.Fatalf("cache TTL = %v, want positive", c.ttls[0])
		}
	})

	t.Run("configured TTL is honoured", func(t *testing.T) {
		c := newSpyCache()
		svc := &service.BusinessService{Store: repo, Cache: c, CacheTTL: 90 * time.Second}
		if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
			t.Fatal(err)
		}
		if c.ttls[0] != 90*time.Second {
			t.Fatalf("cache TTL = %v, want 90s", c.ttls[0])
		}
	})
}

// The service must work with no cache configured at all.
func TestBusinessServiceWorksWithoutACache(t *testing.T) {
	st := store.NewMemoryStore()
	svc := &service.BusinessService{Store: st} // Cache is nil

	if _, err := svc.CreateReview(context.Background(), "biz_1", "user_9", 1, "Not great"); err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetBusiness(context.Background(), "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ReviewCount != 4 || b.Average != 3.5 {
		t.Fatalf("unexpected %+v", b)
	}
}

// stubSubscriptionRepo lets the claim step be observed directly. It is
// mutex-guarded so it can also be driven from concurrent deliveries.
type stubSubscriptionRepo struct {
	mu      sync.Mutex
	sub     model.Subscription
	byRef   map[string]model.Subscription
	claims  []string
	puts    []model.Subscription
	claimed map[string]bool
	getErr  error
}

func (s *stubSubscriptionRepo) GetSubscription(string) (model.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return model.Subscription{}, s.getErr
	}
	return s.sub, nil
}
func (s *stubSubscriptionRepo) GetSubscriptionByReference(ref string) (model.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub, ok := s.byRef[ref]
	if !ok {
		return model.Subscription{}, store.ErrNotFound
	}
	return sub, nil
}
func (s *stubSubscriptionRepo) PutSubscription(sub model.Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts = append(s.puts, sub)
}

// MarkEventProcessed mirrors the real store: an atomic single claim.
func (s *stubSubscriptionRepo) MarkEventProcessed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed == nil {
		s.claimed = map[string]bool{}
	}
	s.claims = append(s.claims, id)
	if s.claimed[id] {
		return false
	}
	s.claimed[id] = true
	return true
}

func (s *stubSubscriptionRepo) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.puts)
}

// The claim must happen only after the event resolves, so an event we cannot
// act on stays available for the provider's retry.
func TestEventIsClaimedOnlyAfterResolution(t *testing.T) {
	repo := &stubSubscriptionRepo{byRef: map[string]model.Subscription{}, getErr: store.ErrNotFound}
	svc := &service.SubscriptionService{Store: repo}

	err := deliver(t, svc, payments.WebhookEvent{
		ID: "charge.success:1", Type: "charge.success", Reference: "ref_missing", Status: "active",
	})
	if !errors.Is(err, service.ErrUnknownSubscription) {
		t.Fatalf("err = %v, want ErrUnknownSubscription", err)
	}
	if len(repo.claims) != 0 {
		t.Fatalf("event was claimed before it resolved: %v", repo.claims)
	}
	if len(repo.puts) != 0 {
		t.Fatalf("an unresolved event still wrote state: %+v", repo.puts)
	}
}
