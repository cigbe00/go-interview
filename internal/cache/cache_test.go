package cache_test

import (
	"context"
	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"sync"
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

func TestMemoryBusinessCacheSupportsConcurrentAccess(t *testing.T) {
	c := cache.NewMemoryBusinessCache()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = c.SetBusiness(context.Background(), model.Business{ID: "biz_x"}, time.Minute)
				_, _, _ = c.GetBusiness(context.Background(), "biz_x")
				_ = c.DeleteBusiness(context.Background(), "biz_x")
			}
		}()
	}
	wg.Wait()
}
