package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
)

var (
	ErrInvalidSubscriptionRequest = errors.New("invalid subscription request")
	// ErrUnknownSubscription means a webhook could not be tied to a local
	// subscription. It is never resolved by guessing from provider-supplied
	// identity fields.
	ErrUnknownSubscription = errors.New("webhook could not be matched to a subscription")
)

const (
	statusPending = "pending"
	statusActive  = "active"
)

// SubscriptionRepository is the persistence surface SubscriptionService
// depends on. MarkEventProcessed must be an atomic claim — it reports whether
// this caller was the one that took the event — because it is what makes
// webhook processing idempotent under concurrent redelivery.
type SubscriptionRepository interface {
	GetSubscription(userID string) (model.Subscription, error)
	GetSubscriptionByReference(reference string) (model.Subscription, error)
	PutSubscription(sub model.Subscription)
	MarkEventProcessed(id string) bool
}

type SubscriptionService struct {
	Store    SubscriptionRepository
	Provider payments.Provider
	Logger   *slog.Logger
}

func (s *SubscriptionService) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *SubscriptionService) Initialize(ctx context.Context, userID, email, plan string, amount int64) (payments.InitializeResponse, error) {
	userID, email, plan = strings.TrimSpace(userID), strings.TrimSpace(email), strings.TrimSpace(plan)
	if userID == "" || email == "" || !strings.Contains(email, "@") {
		return payments.InitializeResponse{}, fmt.Errorf("%w: user_id and a valid email are required", ErrInvalidSubscriptionRequest)
	}
	// Paystack rejects non-positive amounts; catching it here saves a round
	// trip and returns 400 instead of a 502 from the provider.
	if amount <= 0 {
		return payments.InitializeResponse{}, fmt.Errorf("%w: amount must be greater than zero", ErrInvalidSubscriptionRequest)
	}

	// The user ID stays in the reference so a transaction can be traced back to
	// an account from the Paystack dashboard. It must also be unique: two
	// checkouts started at the same instant by one user would otherwise share a
	// reference, which Paystack rejects as a duplicate transaction and which
	// would make the callback ambiguous.
	ref := newID("maoni_" + userID)
	resp, err := s.Provider.Initialize(ctx, payments.InitializeRequest{
		UserID:    userID,
		Email:     email,
		Amount:    amount,
		PlanCode:  plan,
		Reference: ref,
	})
	if err != nil {
		s.logger().ErrorContext(ctx, "paystack initialize failed", "user_id", userID, "reference", ref, "error", err)
		return payments.InitializeResponse{}, err
	}

	// Starting a new checkout must not knock an already-active subscription
	// back to pending — the customer keeps their access until a webhook says
	// otherwise. The reference is still recorded so the callback resolves.
	status := statusPending
	if existing, err := s.Store.GetSubscription(userID); err == nil && existing.Status == statusActive {
		status = statusActive
	}
	s.Store.PutSubscription(model.Subscription{
		UserID:    userID,
		Status:    status,
		Reference: resp.Reference,
		PlanCode:  plan,
		UpdatedAt: time.Now().UTC(),
	})
	return resp, nil
}

// HandleWebhook verifies, resolves and applies a provider callback.
//
// Order matters: the signature is checked against the raw body first, and the
// event is only claimed for processing once it has been resolved to a
// subscription — otherwise an event we could not act on would be marked
// processed and the provider's retry would be discarded.
func (s *SubscriptionService) HandleWebhook(ctx context.Context, body []byte, sig string) error {
	if err := s.Provider.VerifyWebhookSignature(body, sig); err != nil {
		return err
	}

	event, err := s.Provider.ParseWebhook(body)
	if err != nil {
		return err
	}

	// Without an idempotency key a retry cannot be distinguished from a new
	// event. ParseWebhook already enforces this; the check is repeated here so
	// a future provider implementation cannot quietly disable deduplication.
	if event.ID == "" {
		return fmt.Errorf("%w: event has no idempotency key", payments.ErrProvider)
	}

	existing, err := s.resolveSubscription(event)
	if err != nil {
		return err
	}

	// Claim the event before applying it. A duplicate delivery loses the race
	// here and returns without re-applying the change.
	if !s.Store.MarkEventProcessed(event.ID) {
		s.logger().InfoContext(ctx, "ignoring duplicate webhook delivery", "event_id", event.ID, "type", event.Type)
		return nil
	}

	updated := existing
	updated.Status = event.Status
	updated.LastEventID = event.ID
	updated.UpdatedAt = time.Now().UTC()
	// Only overwrite correlation fields the event actually carries, otherwise
	// a lifecycle event without a reference or plan would erase them.
	if event.Reference != "" {
		updated.Reference = event.Reference
	}
	if event.PlanCode != "" {
		updated.PlanCode = event.PlanCode
	}

	s.Store.PutSubscription(updated)
	s.logger().InfoContext(ctx, "applied subscription webhook",
		"event_id", event.ID, "type", event.Type, "user_id", updated.UserID, "status", updated.Status)
	return nil
}

// resolveSubscription maps a webhook onto the local subscription that owns it.
//
// The transaction reference is preferred because we generated and stored it at
// initialize time, so the mapping is ours rather than the provider's. The
// metadata user ID is the same value we sent, echoed back inside the
// HMAC-signed payload, and is only accepted when a subscription for that user
// already exists. The customer email is deliberately not used: it is mutable,
// can be shared or reassigned, and matching on it would let one customer's
// callback mutate another account's subscription.
func (s *SubscriptionService) resolveSubscription(event payments.WebhookEvent) (model.Subscription, error) {
	if event.Reference != "" {
		if sub, err := s.Store.GetSubscriptionByReference(event.Reference); err == nil {
			return sub, nil
		}
	}
	if event.UserID != "" {
		if sub, err := s.Store.GetSubscription(event.UserID); err == nil {
			return sub, nil
		}
	}
	return model.Subscription{}, fmt.Errorf("%w: event %s (type %s)", ErrUnknownSubscription, event.ID, event.Type)
}

func (s *SubscriptionService) Get(userID string) (model.Subscription, error) {
	return s.Store.GetSubscription(strings.TrimSpace(userID))
}
