package service_test

import (
	"context"
	"errors"
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

func TestSubscriptionInitializeValidatesInput(t *testing.T) {
	svc := service.SubscriptionService{Store: store.NewMemoryStore(), Provider: payments.FakeProvider{}}
	tests := []struct {
		userID string
		email  string
		plan   string
		amount int64
	}{
		{"", "user@example.com", "PLN_1", 1},
		{"user_1", "invalid", "PLN_1", 1},
		{"user_1", "user@example.com", "", 1},
		{"user_1", "user@example.com", "PLN_1", 0},
	}
	for _, tt := range tests {
		if _, err := svc.Initialize(context.Background(), tt.userID, tt.email, tt.plan, tt.amount); !errors.Is(err, service.ErrInvalidSubscriptionRequest) {
			t.Fatalf("input=%+v err=%v", tt, err)
		}
	}
}

func TestSubscriptionWebhookUpdatesCorrectUserIdempotently(t *testing.T) {
	st := store.NewMemoryStore()
	p := payments.FakeProvider{
		InitResp: payments.InitializeResponse{Reference: "ref_1", AuthorizationURL: "https://example.test/pay", AccessCode: "code"},
		Event:    payments.WebhookEvent{ID: "charge.success:1", Type: "charge.success", Reference: "ref_1", UserID: "user_1", Email: "different@example.com", PlanCode: "PLN_1", Status: "active"},
	}
	svc := service.SubscriptionService{Store: st, Provider: p}
	if _, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_1", 500000); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "signature"); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "signature"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "active" || sub.LastEventID != "charge.success:1" {
		t.Fatalf("sub=%+v", sub)
	}
	if _, err := st.GetSubscription("different@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("email unexpectedly used as user ID: %v", err)
	}
}

func TestSubscriptionWebhookRejectsCorrelationMismatch(t *testing.T) {
	st := store.NewMemoryStore()
	p := payments.FakeProvider{
		InitResp: payments.InitializeResponse{Reference: "ref_1", AuthorizationURL: "https://example.test/pay", AccessCode: "code"},
		Event:    payments.WebhookEvent{ID: "charge.success:1", Type: "charge.success", Reference: "ref_1", UserID: "attacker", PlanCode: "PLN_1", Status: "active"},
	}
	svc := service.SubscriptionService{Store: st, Provider: p}
	if _, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_1", 500000); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleWebhook(context.Background(), nil, "signature"); !errors.Is(err, service.ErrSubscriptionCorrelation) {
		t.Fatalf("err=%v, want ErrSubscriptionCorrelation", err)
	}
	sub, _ := st.GetSubscription("user_1")
	if sub.Status != "pending" {
		t.Fatalf("sub=%+v", sub)
	}
}

func TestSubscriptionWebhookIgnoresUnsupportedEvent(t *testing.T) {
	svc := service.SubscriptionService{Store: store.NewMemoryStore(), Provider: payments.FakeProvider{Event: payments.WebhookEvent{Type: "invoice.create"}}}
	if err := svc.HandleWebhook(context.Background(), nil, "signature"); err != nil {
		t.Fatal(err)
	}
}
