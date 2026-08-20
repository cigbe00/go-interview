package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
)

var (
	ErrInvalidRating = errors.New("rating must be between 1 and 5")
	ErrUserIDMissing = errors.New("user_id is required")
)

type BusinessService struct {
	Store    *store.MemoryStore
	Cache    cache.BusinessCache
	CacheTTL time.Duration
}

func (s *BusinessService) GetBusiness(ctx context.Context, id string) (model.Business, error) {
	if s.Cache != nil {
		if b, ok, err := s.Cache.GetBusiness(ctx, id); err == nil && ok {
			return b, nil
		} else if err != nil {
			log.Printf("business cache get failed for %q: %v", id, err)
		}
	}
	b, err := s.Store.GetBusiness(id)
	if err != nil {
		return model.Business{}, err
	}
	count, avg := s.Store.ReviewStats(b.ID)
	b.ReviewCount, b.Average = count, avg
	if s.Cache != nil {
		if err := s.Cache.SetBusiness(ctx, b, s.CacheTTL); err != nil {
			log.Printf("business cache set failed for %q: %v", id, err)
		}
	}
	return b, nil
}
func (s *BusinessService) CreateReview(ctx context.Context, businessID, userID string, rating int, body string) (model.Review, error) {
	if rating < 1 || rating > 5 {
		return model.Review{}, ErrInvalidRating
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return model.Review{}, ErrUserIDMissing
	}
	if _, err := s.Store.GetBusinessRaw(businessID); err != nil {
		return model.Review{}, err
	}
	r := model.Review{ID: fmt.Sprintf("rev_%d", time.Now().UnixNano()), BusinessID: businessID, UserID: userID, Rating: rating, Body: strings.TrimSpace(body), CreatedAt: time.Now().UTC()}
	if err := s.Store.SaveReview(r); err != nil {
		return model.Review{}, err
	}
	if s.Cache != nil {
		if err := s.Cache.DeleteBusiness(ctx, businessID); err != nil {
			log.Printf("business cache delete failed for %q: %v", businessID, err)
		}
	}
	return r, nil
}
func (s *BusinessService) ListReviews(businessID string, page, limit int) ([]model.Review, error) {
	if _, err := s.Store.GetBusinessRaw(businessID); err != nil {
		return nil, err
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	return s.Store.ListReviews(businessID, page, limit), nil
}
