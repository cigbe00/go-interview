package service_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	cachepkg "github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

func TestCreateReviewInvalidatesCachedBusinessAndUpdatesAggregates(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemoryStore()
	c := cachepkg.NewMemoryBusinessCache()
	svc := service.BusinessService{Store: st, Cache: c, CacheTTL: time.Minute}

	before, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if before.ReviewCount != 3 {
		t.Fatalf("before=%+v", before)
	}
	if _, err := svc.CreateReview(ctx, "biz_1", " user_4 ", 3, " Fine "); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.GetBusiness(ctx, "biz_1"); err != nil || ok {
		t.Fatalf("cache ok=%v err=%v, want invalidated", ok, err)
	}
	after, err := svc.GetBusiness(ctx, "biz_1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ReviewCount != 4 || math.Abs(after.Average-4) > 0.000001 {
		t.Fatalf("after=%+v", after)
	}
}

func TestListReviewsValidatesBusinessAndDefaultsPagination(t *testing.T) {
	svc := service.BusinessService{Store: store.NewMemoryStore()}
	reviews, err := svc.ListReviews("biz_1", 0, 0)
	if err != nil || len(reviews) != 3 || reviews[0].ID != "rev_3" {
		t.Fatalf("reviews=%+v err=%v", reviews, err)
	}
	if _, err := svc.ListReviews("missing", 1, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

func TestCreateReviewValidatesUserAndRating(t *testing.T) {
	svc := service.BusinessService{Store: store.NewMemoryStore()}
	if _, err := svc.CreateReview(context.Background(), "biz_1", "", 5, "body"); !errors.Is(err, service.ErrUserIDMissing) {
		t.Fatalf("err=%v, want ErrUserIDMissing", err)
	}
	if _, err := svc.CreateReview(context.Background(), "biz_1", "user", 6, "body"); !errors.Is(err, service.ErrInvalidRating) {
		t.Fatalf("err=%v, want ErrInvalidRating", err)
	}
}

type errorCache struct {
	getErr    error
	setErr    error
	deleteErr error
	ttl       time.Duration
}

func (c *errorCache) GetBusiness(context.Context, string) (model.Business, bool, error) {
	return model.Business{}, false, c.getErr
}

func (c *errorCache) SetBusiness(_ context.Context, _ model.Business, ttl time.Duration) error {
	c.ttl = ttl
	return c.setErr
}

func (c *errorCache) DeleteBusiness(context.Context, string) error { return c.deleteErr }

func TestCacheFailuresDoNotFailBusinessOperations(t *testing.T) {
	cacheErr := errors.New("cache unavailable")
	c := &errorCache{getErr: cacheErr, setErr: cacheErr, deleteErr: cacheErr}
	svc := service.BusinessService{Store: store.NewMemoryStore(), Cache: c, CacheTTL: 45 * time.Second}

	if _, err := svc.GetBusiness(context.Background(), "biz_1"); err != nil {
		t.Fatal(err)
	}
	if c.ttl != 45*time.Second {
		t.Fatalf("ttl=%v", c.ttl)
	}
	if _, err := svc.CreateReview(context.Background(), "biz_1", "user_4", 5, "good"); err != nil {
		t.Fatal(err)
	}
}
