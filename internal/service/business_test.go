package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

func TestCreateReviewInvalidatesCachedBusiness(t *testing.T) {
	st := store.NewMemoryStore()
	c := cache.NewMemoryBusinessCache()
	svc := &service.BusinessService{Store: st, Cache: c, CacheTTL: time.Minute}

	if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Items["biz_1"]; !ok {
		t.Fatal("expected business to be cached")
	}
	if _, err := svc.CreateReview(context.Background(), "biz_1", "user_9", 5, "Nice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Items["biz_1"]; ok {
		t.Fatal("cache must be invalidated after a review")
	}

	b, err := svc.GetBusiness(context.Background(), "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ReviewCount != 4 {
		t.Fatalf("review_count = %d, want 4", b.ReviewCount)
	}
}
