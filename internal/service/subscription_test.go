package service_test

import (
	"context"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
)

func TestDuplicateWebhookIsIdempotent(t *testing.T) {
	st := store.NewMemoryStore()
	provider := payments.FakeProvider{Event: payments.WebhookEvent{ID: "evt_1", Email: "user@example.com", Reference: "ref", PlanCode: "monthly", Status: "active"}}
	svc := &service.SubscriptionService{Store: st, Provider: provider}
	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err != nil {
		t.Fatal(err)
	}
	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetSubscription("user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if sub.LastEventID != "evt_1" {
		t.Fatalf("unexpected sub %+v", sub)
	}
}
