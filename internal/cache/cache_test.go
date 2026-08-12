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
