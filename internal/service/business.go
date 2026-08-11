package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
)

var ErrInvalidRating = errors.New("rating must be between 1 and 5")

type BusinessService struct{ Store *store.MemoryStore }

func (s *BusinessService) GetBusiness(id string) (model.Business, error) {
	b, err := s.Store.GetBusiness(id)
	if err != nil {
		return model.Business{}, err
	}
	count, avg := s.Store.ReviewStats(b.ID)
	b.ReviewCount, b.Average = count, avg
	return b, nil
}

func (s *BusinessService) CreateReview(businessID, userID string, rating int, body string) (model.Review, error) {
	if rating < 1 || rating > 5 {
		return model.Review{}, ErrInvalidRating
	}
	if _, err := s.Store.GetBusinessRaw(businessID); err != nil {
		return model.Review{}, err
	}
	r := model.Review{ID: fmt.Sprintf("rev_%d", time.Now().UnixNano()), BusinessID: businessID, UserID: userID, Rating: rating, Body: body, CreatedAt: time.Now().UTC()}
	if err := s.Store.SaveReview(r); err != nil {
		return model.Review{}, err
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
