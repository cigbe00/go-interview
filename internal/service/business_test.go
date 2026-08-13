package service_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

type failingBusinessCache struct{}

func (failingBusinessCache) GetBusiness(context.Context, string) (model.Business, bool, error) {
	return model.Business{}, false, errors.New("cache unavailable")
}
func (failingBusinessCache) SetBusiness(context.Context, model.Business, time.Duration) error {
	return errors.New("cache unavailable")
}
func (failingBusinessCache) DeleteBusiness(context.Context, string) error {
	return errors.New("cache unavailable")
}

func TestBusinessServiceCreatesReviewAndInvalidatesCachedStats(t *testing.T) {
	ctx := context.Background()
	c := cache.NewMemoryBusinessCache()
	svc := service.BusinessService{Store: store.NewMemoryStore(), Cache: c, CacheTTL: time.Minute}

	before, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if before.ReviewCount != 3 {
		t.Fatalf("review count before create = %d, want 3", before.ReviewCount)
	}
	if _, err := svc.CreateReview(ctx, "biz_1", "user_4", 5, "Great"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ReviewCount != 4 || math.Abs(after.Average-4.5) > 0.0001 {
		t.Fatalf("business after create = %+v, want count 4 and average 4.5", after)
	}
}

func TestBusinessServiceTreatsCacheFailureAsNonAuthoritative(t *testing.T) {
	svc := service.BusinessService{Store: store.NewMemoryStore(), Cache: failingBusinessCache{}, CacheTTL: time.Minute}
	if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
		t.Fatalf("cache read/set failure affected store read: %v", err)
	}
	if _, err := svc.CreateReview(context.Background(), "biz_1", "user_4", 5, "Great"); err != nil {
		t.Fatalf("cache delete failure affected store write: %v", err)
	}
}

func TestBusinessServicePaginationStartsAtFirstRecord(t *testing.T) {
	svc := service.BusinessService{Store: store.NewMemoryStore()}
	first, err := svc.ListReviews("biz_1", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.ListReviews("biz_1", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "rev_3" || first[1].ID != "rev_2" {
		t.Fatalf("first page = %+v", first)
	}
	if len(second) != 1 || second[0].ID != "rev_1" {
		t.Fatalf("second page = %+v", second)
	}
}
