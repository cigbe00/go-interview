package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/store"
	"strings"
	"time"
)

var ErrInvalidSubscriptionRequest = errors.New("invalid subscription request")

type SubscriptionService struct {
	Store    *store.MemoryStore
	Provider payments.Provider
}

func (s *SubscriptionService) Initialize(ctx context.Context, userID, email, plan string, amount int64) (payments.InitializeResponse, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(email) == "" || amount < 0 {
		return payments.InitializeResponse{}, ErrInvalidSubscriptionRequest
	}
	ref := fmt.Sprintf("maoni_%s_%d", userID, time.Now().UnixNano())
	resp, err := s.Provider.Initialize(ctx, payments.InitializeRequest{UserID: userID, Email: email, Amount: amount, PlanCode: plan, Reference: ref})
	if err != nil {
		return payments.InitializeResponse{}, err
	}
	s.Store.PutSubscription(model.Subscription{UserID: userID, Status: "pending", Reference: resp.Reference, PlanCode: plan, UpdatedAt: time.Now().UTC()})
	return resp, nil
}
func (s *SubscriptionService) HandleWebhook(ctx context.Context, body []byte, sig string) error {
	if err := s.Provider.VerifyWebhookSignature(body, sig); err != nil {
		return err
	}
	event, err := s.Provider.ParseWebhook(body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(event.UserID) == "" {
		return fmt.Errorf("webhook missing user identity")
	}
	if !s.Store.MarkEventProcessed(event.ID) {
		return nil
	}
	existing, err := s.Store.GetSubscription(event.UserID)
	if errors.Is(err, store.ErrNotFound) {
		s.Store.PutSubscription(model.Subscription{
			UserID:      event.UserID,
			Status:      event.Status,
			Reference:   event.Reference,
			PlanCode:    event.PlanCode,
			LastEventID: event.ID,
			UpdatedAt:   time.Now().UTC(),
		})
		return nil
	}
	if err != nil {
		return err
	}
	existing.Status = event.Status
	existing.LastEventID = event.ID
	existing.UpdatedAt = time.Now().UTC()
	if event.Reference != "" {
		existing.Reference = event.Reference
	}
	if event.PlanCode != "" {
		existing.PlanCode = event.PlanCode
	}
	s.Store.PutSubscription(existing)
	return nil
}
