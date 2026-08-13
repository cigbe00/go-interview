package service_test

import (
	"context"
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

func TestWebhookAppliesToUserFromIdentity(t *testing.T) {
	st := store.NewMemoryStore()
	p := payments.FakeProvider{Event: payments.WebhookEvent{
		ID: "charge.success:ref_1", Type: "charge.success", Reference: "ref_1",
		UserID: "user_1", Email: "attacker@example.com", PlanCode: "PLN_1", Status: "active",
	}}
	svc := service.SubscriptionService{Store: st, Provider: p}

	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "active" || sub.LastEventID != "charge.success:ref_1" {
		t.Fatalf("unexpected %+v", sub)
	}
	if _, err := st.GetSubscription("attacker@example.com"); err == nil {
		t.Fatal("subscription must not be keyed by webhook email")
	}
}

func TestWebhookRejectsMissingUserIdentity(t *testing.T) {
	st := store.NewMemoryStore()
	p := payments.FakeProvider{Event: payments.WebhookEvent{
		ID: "charge.success:ref_1", Type: "charge.success", Reference: "ref_1",
		Email: "buyer@example.com", Status: "active",
	}}
	svc := service.SubscriptionService{Store: st, Provider: p}

	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err == nil {
		t.Fatal("expected error when webhook carries no user identity")
	}
}

func TestWebhookIsIdempotent(t *testing.T) {
	st := store.NewMemoryStore()
	first := payments.FakeProvider{Event: payments.WebhookEvent{
		ID: "charge.success:ref_1", UserID: "user_1", Reference: "ref_1", Status: "active",
	}}
	svc := service.SubscriptionService{Store: st, Provider: first}

	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err != nil {
		t.Fatal(err)
	}
	svc.Provider = payments.FakeProvider{Event: payments.WebhookEvent{
		ID: "charge.success:ref_1", UserID: "user_1", Reference: "ref_1", Status: "cancelled",
	}}
	if err := svc.HandleWebhook(context.Background(), []byte(`{}`), "sig"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "active" {
		t.Fatalf("duplicate delivery must not overwrite status, got %q", sub.Status)
	}
}
