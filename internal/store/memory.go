package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
)

var ErrNotFound = errors.New("not found")

const (
	BusinessesCollection    = "businesses"
	ReviewsCollection       = "reviews"
	UsersCollection         = "users"
	SubscriptionsCollection = "subscriptions"
)

type MemoryStore struct {
	mu            sync.RWMutex
	businesses    map[string]model.Business
	collections   map[string][]model.Review
	users         map[string]model.User // keyed by internal user ID
	usersByGoogle map[string]string     // google sub -> internal user ID
	usersByEmail  map[string]string     // lowercased email -> internal user ID
	nextUserSeq   int
	subscriptions map[string]model.Subscription
	// subsByReference maps a provider transaction reference to the internal
	// user ID that owns it, so a webhook can be resolved back to a user
	// without trusting provider-supplied identity fields.
	subsByReference map[string]string
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
		users:           map[string]model.User{},
		usersByGoogle:   map[string]string{},
		usersByEmail:    map[string]string{},
		subscriptions:   map[string]model.Subscription{},
		subsByReference: map[string]string{},
		processedEvents: map[string]struct{}{},
	}
}

// GetBusiness looks a business up by its primary identifier.
//
// This previously matched on Slug, which made every lookup by the documented
// ID (biz_1) return ErrNotFound.
func (s *MemoryStore) GetBusiness(id string) (model.Business, error) {
	return s.GetBusinessRaw(id)
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

// SaveReview appends to ReviewsCollection. It previously wrote to the
// singular "review" key, so saved reviews were invisible to both
// ListReviews and ReviewStats.
func (s *MemoryStore) SaveReview(r model.Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collections[ReviewsCollection] = append(s.collections[ReviewsCollection], r)
	return nil
}

// ListReviews returns one page of reviews, newest first, along with the total
// number of reviews for the business.
//
// page is 1-based. The offset was previously page*limit, which skipped the
// whole first page for the first (page=1) request.
func (s *MemoryStore) ListReviews(businessID string, page, limit int) ([]model.Review, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	matches := s.reviewsFor(businessID)
	// Ordering must be total, not just by CreatedAt: reviews created in the
	// same instant would otherwise be ordered arbitrarily between requests,
	// which lets a record be skipped on one page and repeated on the next.
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].CreatedAt.Equal(matches[j].CreatedAt) {
			return matches[i].CreatedAt.After(matches[j].CreatedAt)
		}
		return matches[i].ID < matches[j].ID
	})

	total := len(matches)
	start := (page - 1) * limit
	if start >= total {
		return []model.Review{}, total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return append([]model.Review(nil), matches[start:end]...), total
}

// ReviewStats returns the review count and mean rating for a business.
//
// The mean was previously computed as float64(total / count) — integer
// division that truncated before the conversion, so 13/3 reported 4 instead
// of 4.333…
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
	return count, float64(total) / float64(count)
}

// reviewsFor must be called with at least a read lock held.
func (s *MemoryStore) reviewsFor(businessID string) []model.Review {
	var matches []model.Review
	for _, r := range s.collections[ReviewsCollection] {
		if r.BusinessID == businessID {
			matches = append(matches, r)
		}
	}
	return matches
}

// UpsertUser resolves an identity to a stable local user.
//
// The provider subject (Google `sub`) is the account identifier; email is
// treated as mutable profile data. An existing local account that was created
// with the same email is linked to the subject on first sign-in rather than
// being duplicated.
func (s *MemoryStore) UpsertUser(u model.User) model.User {
	s.mu.Lock()
	defer s.mu.Unlock()

	emailKey := normalizeEmail(u.Email)

	if u.GoogleID != "" {
		if id, ok := s.usersByGoogle[u.GoogleID]; ok {
			return s.updateUserLocked(id, u, emailKey)
		}
	}
	if emailKey != "" {
		if id, ok := s.usersByEmail[emailKey]; ok {
			return s.updateUserLocked(id, u, emailKey)
		}
	}

	if u.ID == "" {
		s.nextUserSeq++
		u.ID = fmt.Sprintf("usr_%d", s.nextUserSeq)
	}
	s.users[u.ID] = u
	if u.GoogleID != "" {
		s.usersByGoogle[u.GoogleID] = u.ID
	}
	if emailKey != "" {
		s.usersByEmail[emailKey] = u.ID
	}
	return u
}

// updateUserLocked merges incoming profile data into an existing account.
// Callers must hold the write lock.
func (s *MemoryStore) updateUserLocked(id string, incoming model.User, emailKey string) model.User {
	existing := s.users[id]
	if incoming.GoogleID != "" && existing.GoogleID != incoming.GoogleID {
		existing.GoogleID = incoming.GoogleID
		s.usersByGoogle[incoming.GoogleID] = id
	}
	if incoming.Email != "" && existing.Email != incoming.Email {
		delete(s.usersByEmail, normalizeEmail(existing.Email))
		existing.Email = incoming.Email
		s.usersByEmail[emailKey] = id
	}
	if incoming.Name != "" {
		existing.Name = incoming.Name
	}
	s.users[id] = existing
	return existing
}

func (s *MemoryStore) GetUser(id string) (model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return model.User{}, ErrNotFound
	}
	return u, nil
}

func (s *MemoryStore) PutSubscription(sub model.Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[sub.UserID] = sub
	if sub.Reference != "" {
		s.subsByReference[sub.Reference] = sub.UserID
	}
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

// GetSubscriptionByReference resolves the subscription that owns a provider
// transaction reference.
func (s *MemoryStore) GetSubscriptionByReference(reference string) (model.Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	userID, ok := s.subsByReference[reference]
	if !ok {
		return model.Subscription{}, ErrNotFound
	}
	sub, ok := s.subscriptions[userID]
	if !ok {
		return model.Subscription{}, ErrNotFound
	}
	return sub, nil
}

// MarkEventProcessed records an event ID and reports whether this call was the
// one that claimed it. A repeat delivery returns false.
func (s *MemoryStore) MarkEventProcessed(id string) bool {
	if id == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.processedEvents[id]; ok {
		return false
	}
	s.processedEvents[id] = struct{}{}
	return true
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
