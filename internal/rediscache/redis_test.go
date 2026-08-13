package rediscache_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/rediscache"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

// newRedisCache connects to the local Docker Redis from docker-compose.yml.
// The test is skipped when Redis is not running, so `go test ./...` stays green
// without Docker; set REDIS_ADDR to point at a different local instance.
func newRedisCache(t *testing.T) *rediscache.BusinessCache {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping redis integration test in short mode")
	}
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	rc := rediscache.New(addr, os.Getenv("REDIS_PASSWORD"), 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := rc.Ping(ctx); err != nil {
		_ = rc.Close()
		t.Skipf("redis not reachable at %s (start it with `make redis-up`): %v", addr, err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc
}

func TestRedisCacheRoundTripAndDelete(t *testing.T) {
	rc := newRedisCache(t)
	ctx := context.Background()
	b := model.Business{ID: "biz_integration_test", Name: "Integration", Slug: "integration", ReviewCount: 2, Average: 4.5}
	t.Cleanup(func() { _ = rc.DeleteBusiness(ctx, b.ID) })

	if err := rc.SetBusiness(ctx, b, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok, err := rc.GetBusiness(ctx, b.ID)
	if err != nil || !ok {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	// The derived fields are what make a stale entry dangerous, so they have
	// to survive the round trip intact.
	if got.ReviewCount != 2 || got.Average != 4.5 || got.Name != "Integration" {
		t.Fatalf("round trip lost data: %+v", got)
	}

	if err := rc.DeleteBusiness(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := rc.GetBusiness(ctx, b.ID); err != nil || ok {
		t.Fatalf("entry survived deletion: ok=%v err=%v", ok, err)
	}
	// Deleting an absent key is not an error.
	if err := rc.DeleteBusiness(ctx, "biz_never_cached"); err != nil {
		t.Fatalf("delete of a missing key returned %v", err)
	}
}

func TestRedisCacheTTLIsApplied(t *testing.T) {
	rc := newRedisCache(t)
	ctx := context.Background()
	b := model.Business{ID: "biz_integration_ttl", Name: "TTL"}
	t.Cleanup(func() { _ = rc.DeleteBusiness(ctx, b.ID) })

	if err := rc.SetBusiness(ctx, b, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := rc.GetBusiness(ctx, b.ID); !ok {
		t.Fatal("entry missing immediately after write")
	}
	time.Sleep(500 * time.Millisecond)
	if _, ok, _ := rc.GetBusiness(ctx, b.ID); ok {
		t.Fatal("entry outlived its TTL")
	}
}

// The stale-cache fix, exercised against real Redis rather than a fake.
func TestReviewWriteInvalidatesRedisEntry(t *testing.T) {
	rc := newRedisCache(t)
	ctx := context.Background()
	st := store.NewMemoryStore()
	svc := &service.BusinessService{Store: st, Cache: rc, CacheTTL: 5 * time.Minute}
	t.Cleanup(func() { _ = rc.DeleteBusiness(ctx, "biz_1") })

	// Start from a known-cold cache.
	if err := rc.DeleteBusiness(ctx, "biz_1"); err != nil {
		t.Fatal(err)
	}
	before, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if before.ReviewCount != 3 {
		t.Fatalf("review count = %d, want 3", before.ReviewCount)
	}

	if _, err := svc.CreateReview(ctx, "biz_1", "user_9", 1, "Not great"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := rc.GetBusiness(ctx, "biz_1"); err != nil || ok {
		t.Fatalf("redis entry survived a write that changed its derived data (ok=%v err=%v)", ok, err)
	}

	after, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ReviewCount != 4 || after.Average != 3.5 {
		t.Fatalf("stale data served after invalidation: %+v", after)
	}
}

// The Redis client must satisfy the cache interface the service depends on.
var _ cache.BusinessCache = (*rediscache.BusinessCache)(nil)
