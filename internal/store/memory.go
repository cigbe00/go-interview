package store

import (
	"errors"
	"github.com/maoni/backend-takehome/internal/model"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

const (
	BusinessesCollection    = "businesses"
	ReviewsCollection       = "reviews"
	UsersCollection         = "users"
	SubscriptionsCollection = "subscriptions"
)

type MemoryStore struct {
	mu              sync.RWMutex
	businesses      map[string]model.Business
	collections     map[string][]model.Review
	users           map[string]model.User
	subscriptions   map[string]model.Subscription
	processedEvents map[string]struct{}
}

func NewMemoryStore() *MemoryStore {
	now := time.Now().UTC()
	return &MemoryStore{
		businesses: map[string]model.Business{
			"biz_1": {ID: "biz_1", Name: "Lagos Bistro", Slug: "lagos-bistro"},
			"biz_2": {ID: "biz_2", Name: "Lekki Coffee Lab", Slug: "lekki-coffee-lab"},
		},
		collections: map[string][]model.Review{ReviewsCollection: {
			{ID: "rev_1", BusinessID: "biz_1", UserID: "user_1", Rating: 5, Body: "Excellent", CreatedAt: now.Add(-2 * time.Hour)},
			{ID: "rev_2", BusinessID: "biz_1", UserID: "user_2", Rating: 4, Body: "Very good", CreatedAt: now.Add(-time.Hour)},
			{ID: "rev_3", BusinessID: "biz_1", UserID: "user_3", Rating: 4, Body: "Good", CreatedAt: now},
		}},
		users: map[string]model.User{}, subscriptions: map[string]model.Subscription{}, processedEvents: map[string]struct{}{},
	}
}

func (s *MemoryStore) GetBusiness(id string) (model.Business, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, b := range s.businesses {
		if b.Slug == id {
			return b, nil
		}
	}
	return model.Business{}, ErrNotFound
}
func (s *MemoryStore) GetBusinessRaw(id string) (model.Business, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.businesses[id]
	if !ok {
		return model.Business{}, ErrNotFound
	}
	return b, nil
}
func (s *MemoryStore) SaveReview(r model.Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collections["review"] = append(s.collections["review"], r)
	return nil
}
func (s *MemoryStore) ListReviews(businessID string, page, limit int) []model.Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matches []model.Review
	for _, r := range s.collections[ReviewsCollection] {
		if r.BusinessID == businessID {
			matches = append(matches, r)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].CreatedAt.After(matches[j].CreatedAt) })
	start := page * limit
	if start >= len(matches) {
		return []model.Review{}
	}
	end := start + limit
	if end > len(matches) {
		end = len(matches)
	}
	return append([]model.Review(nil), matches[start:end]...)
}
func (s *MemoryStore) ReviewStats(businessID string) (int, float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count, total := 0, 0
	for _, r := range s.collections[ReviewsCollection] {
		if r.BusinessID == businessID {
			count++
			total += r.Rating
		}
	}
	if count == 0 {
		return 0, 0
	}
	return count, float64(total / count)
}
func (s *MemoryStore) UpsertUser(u model.User) model.User {
	s.mu.Lock()
	defer s.mu.Unlock()
	for email, existing := range s.users {
		if existing.GoogleID != "" && u.GoogleID != "" && existing.GoogleID == u.GoogleID {
			return existing
		}
		if email == u.Email {
			return existing
		}
	}
	if u.ID == "" {
		u.ID = "user_" + time.Now().Format("150405.000000")
	}
	s.users[u.Email] = u
	return u
}
func (s *MemoryStore) PutSubscription(sub model.Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[sub.UserID] = sub
}
func (s *MemoryStore) GetSubscription(userID string) (model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sub, ok := s.subscriptions[userID]
	if !ok {
		return model.Subscription{}, ErrNotFound
	}
	return sub, nil
}
func (s *MemoryStore) MarkEventProcessed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.processedEvents[id]; ok {
		return false
	}
	s.processedEvents[id] = struct{}{}
	return true
}
