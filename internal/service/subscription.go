package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strings"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/store"
)

var ErrInvalidSubscriptionRequest = errors.New("invalid subscription request")
var ErrSubscriptionCorrelation = errors.New("webhook does not match an initialized subscription")

type SubscriptionService struct {
	Store    *store.MemoryStore
	Provider payments.Provider
}

func (s *SubscriptionService) Initialize(ctx context.Context, userID, email, plan string, amount int64) (payments.InitializeResponse, error) {
	userID = strings.TrimSpace(userID)
	email = strings.ToLower(strings.TrimSpace(email))
	plan = strings.TrimSpace(plan)
	if userID == "" || !validEmail(email) || plan == "" || amount <= 0 {
		return payments.InitializeResponse{}, ErrInvalidSubscriptionRequest
	}
	ref := fmt.Sprintf("maoni_%s_%d", userID, time.Now().UnixNano())
	resp, err := s.Provider.Initialize(ctx, payments.InitializeRequest{UserID: userID, Email: email, Amount: amount, PlanCode: plan, Reference: ref})
	if err != nil {
		return payments.InitializeResponse{}, err
	}
	if resp.Reference == "" || resp.AuthorizationURL == "" || resp.AccessCode == "" {
		return payments.InitializeResponse{}, fmt.Errorf("%w: incomplete initialization response", payments.ErrProvider)
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
	if event.Type != "charge.success" {
		return nil
	}
	sub, err := s.Store.GetSubscriptionByReference(event.Reference)
	if err != nil {
		return fmt.Errorf("%w: reference %q", ErrSubscriptionCorrelation, event.Reference)
	}
	if sub.UserID != event.UserID || (event.PlanCode != "" && sub.PlanCode != event.PlanCode) {
		return fmt.Errorf("%w: %w", ErrSubscriptionCorrelation, store.ErrSubscriptionMismatch)
	}
	applied, err := s.Store.ApplySubscriptionEvent(event.ID, event.UserID, event.Reference, event.Status, event.PlanCode)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSubscriptionCorrelation, err)
	}
	if applied {
		log.Printf("applied payment event %q to subscription for user %q", event.ID, event.UserID)
	} else {
		log.Printf("ignored duplicate payment event %q", event.ID)
	}
	return nil
}

func validEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}
