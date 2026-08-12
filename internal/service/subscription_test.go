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
