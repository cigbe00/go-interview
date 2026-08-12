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

type failingBusinessCache struct{}

func (failingBusinessCache) GetBusiness(context.Context, string) (model.Business, bool, error) {
	return model.Business{}, false, errors.New("redis down")
}
func (failingBusinessCache) SetBusiness(context.Context, model.Business, time.Duration) error {
	return errors.New("redis down")
}
func (failingBusinessCache) DeleteBusiness(context.Context, string) error {
	return errors.New("redis down")
}

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

func TestGetBusinessServesFromCache(t *testing.T) {
	st := store.NewMemoryStore()
	c := cache.NewMemoryBusinessCache()
	c.Items["biz_ghost"] = model.Business{ID: "biz_ghost", Name: "Ghost", ReviewCount: 99, Average: 4.9}
	svc := &service.BusinessService{Store: st, Cache: c, CacheTTL: time.Minute}

	b, err := svc.GetBusiness(context.Background(), "biz_ghost")
	if err != nil {
		t.Fatal(err)
	}
	// biz_ghost is not in the store, so a non-empty result proves the cache hit path
	if b.Name != "Ghost" || b.ReviewCount != 99 {
		t.Fatalf("expected cached business, got %+v", b)
	}
}

func TestGetBusinessFallsBackWhenCacheReadFails(t *testing.T) {
	st := store.NewMemoryStore()
	svc := &service.BusinessService{Store: st, Cache: failingBusinessCache{}, CacheTTL: time.Minute}

	b, err := svc.GetBusiness(context.Background(), "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != "biz_1" || b.ReviewCount != 3 {
		t.Fatalf("expected store business, got %+v", b)
	}
}

func TestGetBusinessSurvivesCacheWriteFailure(t *testing.T) {
	st := store.NewMemoryStore()
	svc := &service.BusinessService{Store: st, Cache: failingBusinessCache{}, CacheTTL: time.Minute}

	b, err := svc.GetBusiness(context.Background(), "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if b.ReviewCount != 3 {
		t.Fatalf("expected store business, got %+v", b)
	}
}
