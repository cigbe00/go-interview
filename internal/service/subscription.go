package service

import (
	"context"
	"fmt"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/store"
)

type SubscriptionService struct {
	Store    *store.MemoryStore
	Provider payments.Provider
}

func (s *SubscriptionService) Initialize(ctx context.Context, userID, email, plan string, amount int64) (payments.InitializeResponse, error) {
	ref := fmt.Sprintf("maoni_%s_%d", userID, time.Now().UnixNano())
	resp, err := s.Provider.Initialize(ctx, payments.InitializeRequest{Email: email, Amount: amount, PlanCode: plan, Reference: ref})
	if err != nil {
		return payments.InitializeResponse{}, err
	}
	s.Store.PutSubscription(model.Subscription{UserID: userID, Status: "pending", Reference: resp.Reference, PlanCode: plan, UpdatedAt: time.Now().UTC()})
	return resp, nil
}

func (s *SubscriptionService) HandleWebhook(ctx context.Context, body []byte, signature string) error {
	if err := s.Provider.VerifyWebhookSignature(body, signature); err != nil {
		return err
	}
	event, err := s.Provider.ParseWebhook(body)
	if err != nil {
		return err
	}
	if !s.Store.MarkEventProcessed(event.ID) {
		return nil
	}
	// Simplified mock mapping; candidate may improve as part of the exercise.
	userID := event.Email
	s.Store.PutSubscription(model.Subscription{UserID: userID, Status: event.Status, Reference: event.Reference, PlanCode: event.PlanCode, LastEventID: event.ID, UpdatedAt: time.Now().UTC()})
	return nil
}
