package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

type MockCache struct {
	data    map[string]model.Business
	failGet bool
	failSet bool
	failDel bool
}

func NewMockCache() *MockCache {
	return &MockCache{data: make(map[string]model.Business)}
}

func (m *MockCache) GetBusiness(ctx context.Context, id string) (model.Business, bool, error) {
	if m.failGet {
		return model.Business{}, false, errors.New("redis connection refused")
	}
	b, ok := m.data[id]
	return b, ok, nil
}

func (m *MockCache) SetBusiness(ctx context.Context, b model.Business, ttl time.Duration) error {
	if m.failSet {
		return errors.New("redis write error")
	}
	m.data[b.ID] = b
	if b.Slug != "" {
		m.data[b.Slug] = b
	}
	return nil
}

func (m *MockCache) DeleteBusiness(ctx context.Context, id string) error {
	if m.failDel {
		return errors.New("redis del error")
	}
	delete(m.data, id)
	return nil
}

func TestBusinessService_CacheInvalidationOnReview(t *testing.T) {
	ms := store.NewMemoryStore()
	cache := NewMockCache()
	svc := service.NewBusinessService(ms, cache, 5*time.Minute)
	ctx := context.Background()

	biz1, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if biz1.ReviewCount != 3 {
		t.Errorf("expected initial review count 3, got %d", biz1.ReviewCount)
	}

	if len(cache.data) == 0 {
		t.Fatalf("expected business to be cached")
	}

	_, err = svc.CreateReview(ctx, "biz_1", "user_99", 5, "Amazing experience!")
	if err != nil {
		t.Fatalf("failed to create review: %v", err)
	}

	if _, exists := cache.data["biz_1"]; exists {
		t.Errorf("expected cache key 'biz_1' to be deleted after new review")
	}

	biz1Updated, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatalf("unexpected error on second fetch: %v", err)
	}
	if biz1Updated.ReviewCount != 4 {
		t.Errorf("expected updated review count 4, got %d", biz1Updated.ReviewCount)
	}
}

func TestBusinessService_RedisFailureFallback(t *testing.T) {
	ms := store.NewMemoryStore()
	cache := NewMockCache()
	cache.failGet = true
	svc := service.NewBusinessService(ms, cache, 5*time.Minute)
	ctx := context.Background()

	biz, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatalf("expected successful response despite Redis failure, got: %v", err)
	}
	if biz.ID != "biz_1" {
		t.Errorf("expected biz_1, got %s", biz.ID)
	}
}
