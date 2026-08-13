package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
)

const (
	maxReviewBodyLen = 5000
	// cacheOpTimeout bounds a single cache round trip so a slow or wedged
	// Redis cannot hold a request open for the whole server write timeout.
	cacheOpTimeout = 250 * time.Millisecond
)

var (
	ErrInvalidRating = errors.New("rating must be between 1 and 5")
	ErrUserRequired  = errors.New("user_id is required")
	ErrBodyTooLong   = fmt.Errorf("review body must be at most %d characters", maxReviewBodyLen)
)

type BusinessService struct {
	Store    *store.MemoryStore
	Cache    cache.BusinessCache
	CacheTTL time.Duration
	Logger   *slog.Logger
}

func (s *BusinessService) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *BusinessService) GetBusiness(ctx context.Context, id string) (model.Business, error) {
	if s.Cache != nil {
		cctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
		b, ok, err := s.Cache.GetBusiness(cctx, id)
		cancel()
		switch {
		case err != nil:
			// A cache failure must degrade to a store read, never to an error.
			s.logger().WarnContext(ctx, "business cache read failed", "business_id", id, "error", err)
		case ok:
			return b, nil
		}
	}

	b, err := s.Store.GetBusiness(id)
	if err != nil {
		return model.Business{}, err
	}
	b.ReviewCount, b.Average = s.Store.ReviewStats(b.ID)

	if s.Cache != nil {
		cctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
		if err := s.Cache.SetBusiness(cctx, b, s.CacheTTL); err != nil {
			s.logger().WarnContext(ctx, "business cache write failed", "business_id", b.ID, "error", err)
		}
		cancel()
	}
	return b, nil
}

func (s *BusinessService) CreateReview(ctx context.Context, businessID, userID string, rating int, body string) (model.Review, error) {
	userID = strings.TrimSpace(userID)
	body = strings.TrimSpace(body)
	if userID == "" {
		return model.Review{}, ErrUserRequired
	}
	if rating < 1 || rating > 5 {
		return model.Review{}, ErrInvalidRating
	}
	if len(body) > maxReviewBodyLen {
		return model.Review{}, ErrBodyTooLong
	}
	if _, err := s.Store.GetBusinessRaw(businessID); err != nil {
		return model.Review{}, err
	}

	r := model.Review{
		ID:         fmt.Sprintf("rev_%d", time.Now().UnixNano()),
		BusinessID: businessID,
		UserID:     userID,
		Rating:     rating,
		Body:       body,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.Store.SaveReview(r); err != nil {
		return model.Review{}, err
	}

	// The cached business embeds review_count and average_rating, so a new
	// review makes the cached copy stale and it has to be invalidated here.
	// The review is already durable at this point: a cache failure is logged
	// and the request still succeeds rather than corrupting or rejecting a
	// write that happened. The entry is TTL-bounded, so the blast radius of a
	// failed delete is bounded staleness, not permanent divergence.
	s.invalidateBusiness(ctx, businessID)
	return r, nil
}

func (s *BusinessService) invalidateBusiness(ctx context.Context, businessID string) {
	if s.Cache == nil {
		return
	}
	// Deliberately not derived from the request context: the write has
	// committed, so invalidation should still run if the client disconnected.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cacheOpTimeout)
	defer cancel()
	if err := s.Cache.DeleteBusiness(cctx, businessID); err != nil {
		s.logger().ErrorContext(ctx, "business cache invalidation failed; serving stale until TTL expiry",
			"business_id", businessID, "error", err)
	}
}

// ListReviews returns one page of reviews plus the total count for the
// business. It reports store.ErrNotFound for an unknown business instead of an
// empty page, so a typo in the ID is not indistinguishable from "no reviews".
func (s *BusinessService) ListReviews(ctx context.Context, businessID string, page, limit int) ([]model.Review, int, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}
	if _, err := s.Store.GetBusinessRaw(businessID); err != nil {
		return nil, 0, err
	}
	reviews, total := s.Store.ListReviews(businessID, page, limit)
	return reviews, total, nil
}
