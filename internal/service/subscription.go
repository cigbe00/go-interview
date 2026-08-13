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
	userID, email, plan = strings.TrimSpace(userID), strings.TrimSpace(email), strings.TrimSpace(plan)
	if userID == "" || email == "" || plan == "" || amount <= 0 {
		return payments.InitializeResponse{}, ErrInvalidSubscriptionRequest
	}
	ref := fmt.Sprintf("maoni_%s_%d", userID, time.Now().UnixNano())
	resp, err := s.Provider.Initialize(ctx, payments.InitializeRequest{UserID: userID, Email: email, Amount: amount, PlanCode: plan, Reference: ref})
	if err != nil {
		return payments.InitializeResponse{}, err
	}
	if strings.TrimSpace(resp.Reference) == "" || strings.TrimSpace(resp.AuthorizationURL) == "" || strings.TrimSpace(resp.AccessCode) == "" {
		return payments.InitializeResponse{}, payments.ErrProvider
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
	if event.ID == "" || event.UserID == "" || event.Reference == "" || event.Status == "" {
		return payments.ErrInvalidWebhook
	}
	if err := s.Store.ApplySubscriptionEvent(event.ID, event.UserID, event.Reference, event.PlanCode, event.Status, time.Now().UTC()); err != nil {
		return fmt.Errorf("apply subscription event: %w", err)
	}
	return nil
}
