package cache_test

import (
	"context"
	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"testing"
	"time"
)

func TestMemoryBusinessCacheRoundTrip(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	b := model.Business{ID: "biz_x", Name: "X"}
	if err := c.SetBusiness(context.Background(), b, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.GetBusiness(context.Background(), "biz_x")
	if err != nil || !ok || got.ID != "biz_x" {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
}

// NoopBusinessCache is what the application runs with when Redis is
// unreachable at startup, so its behaviour is production behaviour: never
// report a hit, never return an error, never fail a request.
func TestNoopBusinessCacheAlwaysMisses(t *testing.T) {
	var c cache.BusinessCache = cache.NoopBusinessCache{}
	ctx := context.Background()

	if err := c.SetBusiness(ctx, model.Business{ID: "biz_1"}, time.Minute); err != nil {
		t.Fatalf("SetBusiness returned %v", err)
	}
	got, ok, err := c.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatalf("GetBusiness returned %v", err)
	}
	if ok {
		t.Fatalf("the noop cache reported a hit: %+v", got)
	}
	if err := c.DeleteBusiness(ctx, "biz_1"); err != nil {
		t.Fatalf("DeleteBusiness returned %v", err)
	}
}

func TestMemoryBusinessCacheDeleteAndLen(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	ctx := context.Background()

	if c.Len() != 0 {
		t.Fatalf("new cache has %d entries", c.Len())
	}
	for _, id := range []string{"biz_1", "biz_2"} {
		if err := c.SetBusiness(ctx, model.Business{ID: id}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}

	if err := c.DeleteBusiness(ctx, "biz_1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.GetBusiness(ctx, "biz_1"); ok {
		t.Fatal("deleted entry is still readable")
	}
	if _, ok, _ := c.GetBusiness(ctx, "biz_2"); !ok {
		t.Fatal("delete removed the wrong entry")
	}
	// Deleting a key that was never cached is not an error.
	if err := c.DeleteBusiness(ctx, "biz_never"); err != nil {
		t.Fatalf("deleting a missing key returned %v", err)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
}

// Overwriting an entry must replace it, not accumulate copies: this is the
// path a cache refresh after invalidation takes.
func TestMemoryBusinessCacheOverwrites(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	ctx := context.Background()

	if err := c.SetBusiness(ctx, model.Business{ID: "biz_1", ReviewCount: 3}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := c.SetBusiness(ctx, model.Business{ID: "biz_1", ReviewCount: 4}, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := c.GetBusiness(ctx, "biz_1")
	if !ok || got.ReviewCount != 4 {
		t.Fatalf("got %+v ok=%v, want the newer entry", got, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("Len = %d, want 1", c.Len())
	}
}
