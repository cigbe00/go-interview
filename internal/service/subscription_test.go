package service_test

import (
	"context"
	"errors"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
)

func TestSubscriptionInitializePersistsPendingState(t *testing.T) {
	st := store.NewMemoryStore()
	p := payments.FakeProvider{InitResp: payments.InitializeResponse{Reference: "ref_1", AuthorizationURL: "https://example.test/pay", AccessCode: "code"}}
	svc := service.SubscriptionService{Store: st, Provider: p}
	_, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_x", 500000)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "pending" || sub.Reference != "ref_1" {
		t.Fatalf("unexpected %+v", sub)
	}
}

func TestSubscriptionWebhookCorrelatesByStableUserMetadataAndReference(t *testing.T) {
	st := store.NewMemoryStore()
	initProvider := payments.FakeProvider{InitResp: payments.InitializeResponse{Reference: "ref_1", AuthorizationURL: "https://example.test/pay", AccessCode: "code"}}
	svc := service.SubscriptionService{Store: st, Provider: initProvider}
	if _, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_x", 500000); err != nil {
		t.Fatal(err)
	}

	svc.Provider = payments.FakeProvider{Event: payments.WebhookEvent{ID: "charge.success:1", Type: "charge.success", UserID: "user_1", Email: "not-the-user-id@example.com", Reference: "ref_1", PlanCode: "PLN_x", Status: "active"}}
	if err := svc.HandleWebhook(context.Background(), []byte("payload"), "signature"); err != nil {
		t.Fatal(err)
	}
	first, err := st.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "active" || first.LastEventID != "charge.success:1" {
		t.Fatalf("subscription = %+v", first)
	}
	if err := svc.HandleWebhook(context.Background(), []byte("payload"), "signature"); err != nil {
		t.Fatalf("duplicate event returned error: %v", err)
	}
	second, _ := st.GetSubscription("user_1")
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatal("duplicate event was applied twice")
	}
}

func TestSubscriptionWebhookRejectsMismatchedReference(t *testing.T) {
	st := store.NewMemoryStore()
	st.PutSubscription(model.Subscription{UserID: "user_1", Reference: "expected", PlanCode: "PLN_x"})
	svc := service.SubscriptionService{Store: st, Provider: payments.FakeProvider{Event: payments.WebhookEvent{ID: "event-1", UserID: "user_1", Reference: "attacker-reference", PlanCode: "PLN_x", Status: "success"}}}
	err := svc.HandleWebhook(context.Background(), []byte("payload"), "signature")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error = %v, want correlation failure", err)
	}
}
