package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
)

var ErrInvalidRating = errors.New("rating must be between 1 and 5")

type BusinessService struct {
	Store    *store.MemoryStore
	Cache    cache.BusinessCache
	CacheTTL time.Duration
}

func NewBusinessService(s *store.MemoryStore, c cache.BusinessCache, ttl time.Duration) *BusinessService {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &BusinessService{
		Store:    s,
		Cache:    c,
		CacheTTL: ttl,
	}
}

func (s *BusinessService) GetBusiness(ctx context.Context, id string) (model.Business, error) {
	b, err := s.Store.GetBusiness(id)
	if err != nil {
		return model.Business{}, err
	}

	if s.Cache != nil {
		if b, ok, err := s.Cache.GetBusiness(ctx, b.ID); err == nil && ok {
			return b, nil
		}
	}

	count, avg := s.Store.ReviewStats(b.ID)
	b.ReviewCount, b.Average = count, avg
	if s.Cache != nil {
		_ = s.Cache.SetBusiness(ctx, b, s.CacheTTL)
	}
	return b, nil
}
func (s *BusinessService) CreateReview(ctx context.Context, businessID, userID string, rating int, body string) (model.Review, error) {
	if rating < 1 || rating > 5 {
		return model.Review{}, ErrInvalidRating
	}

	b, err := s.Store.GetBusinessRaw(businessID)
	if err != nil {
		return model.Review{}, err
	}

	r := model.Review{ID: fmt.Sprintf("rev_%d", time.Now().UnixNano()), BusinessID: b.ID, UserID: userID, Rating: rating, Body: body, CreatedAt: time.Now().UTC()}
	if err := s.Store.SaveReview(r); err != nil {
		return model.Review{}, err
	}

	if s.Cache != nil {
		_ = s.Cache.DeleteBusiness(ctx, b.ID)
		if b.Slug != "" {
			_ = s.Cache.DeleteBusiness(ctx, b.Slug)
		}
	}

	return r, nil
}
func (s *BusinessService) ListReviews(businessID string, page, limit int) []model.Review {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	return s.Store.ListReviews(businessID, page, limit)
}
