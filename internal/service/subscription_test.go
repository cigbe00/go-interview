package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
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

// initialized returns a service with one pending subscription for user_1 under
// reference ref_1.
func initialized(t *testing.T) (*service.SubscriptionService, *store.MemoryStore) {
	t.Helper()
	st := store.NewMemoryStore()
	svc := &service.SubscriptionService{
		Store:    st,
		Provider: payments.FakeProvider{InitResp: payments.InitializeResponse{Reference: "ref_1", AuthorizationURL: "https://example.test/pay"}},
	}
	if _, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_x", 500000); err != nil {
		t.Fatal(err)
	}
	return svc, st
}

func deliver(t *testing.T, svc *service.SubscriptionService, event payments.WebhookEvent) error {
	t.Helper()
	svc.Provider = payments.FakeProvider{Event: event}
	return svc.HandleWebhook(context.Background(), []byte(`{"event":"charge.success"}`), "sig")
}

func TestWebhookResolvesSubscriptionByReference(t *testing.T) {
	svc, st := initialized(t)

	err := deliver(t, svc, payments.WebhookEvent{
		ID: "charge.success:1", Type: "charge.success", Reference: "ref_1",
		// A different customer email on the payload must not matter.
		Email: "attacker@example.com", PlanCode: "PLN_x", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}

	sub, err := st.GetSubscription("user_1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "active" || sub.LastEventID != "charge.success:1" {
		t.Fatalf("unexpected %+v", sub)
	}
}

func TestWebhookResolvesSubscriptionByMetadataUserID(t *testing.T) {
	svc, st := initialized(t)

	err := deliver(t, svc, payments.WebhookEvent{
		ID: "subscription.create:7", Type: "subscription.create",
		UserID: "user_1", PlanCode: "PLN_x", Status: "active",
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub, _ := st.GetSubscription("user_1"); sub.Status != "active" {
		t.Fatalf("unexpected %+v", sub)
	}
}

// The customer email is provider-controlled, mutable and shareable. Matching on
// it would let one customer's callback mutate another account, so an event that
// carries nothing but an email must not be applied.
func TestWebhookDoesNotIdentifyUserByEmail(t *testing.T) {
	svc, st := initialized(t)

	err := deliver(t, svc, payments.WebhookEvent{
		ID: "charge.success:2", Type: "charge.success",
		Email: "user@example.com", Status: "active",
	})
	if !errors.Is(err, service.ErrUnknownSubscription) {
		t.Fatalf("err = %v, want ErrUnknownSubscription", err)
	}

	// The original subscription is untouched, and no record was created under
	// the email as if it were a user ID.
	if sub, _ := st.GetSubscription("user_1"); sub.Status != "pending" {
		t.Fatalf("subscription was modified: %+v", sub)
	}
	if _, err := st.GetSubscription("user@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a subscription was created keyed by email address")
	}
}

// A duplicate delivery must not re-apply the change.
func TestWebhookIsIdempotent(t *testing.T) {
	svc, st := initialized(t)

	event := payments.WebhookEvent{
		ID: "charge.success:1", Type: "charge.success", Reference: "ref_1",
		PlanCode: "PLN_x", Status: "active",
	}
	if err := deliver(t, svc, event); err != nil {
		t.Fatal(err)
	}

	// Simulate the subscription moving on after the first delivery. Replaying
	// the original event must not resurrect the earlier state.
	cancelled, _ := st.GetSubscription("user_1")
	cancelled.Status = "cancelled"
	st.PutSubscription(cancelled)

	if err := deliver(t, svc, event); err != nil {
		t.Fatalf("duplicate delivery should be accepted silently: %v", err)
	}
	if sub, _ := st.GetSubscription("user_1"); sub.Status != "cancelled" {
		t.Fatalf("duplicate delivery re-applied the event: %+v", sub)
	}
}

// Events that differ only by type share a provider object ID, so they must not
// collide in the idempotency key.
func TestDistinctEventTypesAreNotDeduplicatedTogether(t *testing.T) {
	svc, st := initialized(t)

	if err := deliver(t, svc, payments.WebhookEvent{
		ID: "charge.success:1", Type: "charge.success", Reference: "ref_1", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := deliver(t, svc, payments.WebhookEvent{
		ID: "subscription.disable:1", Type: "subscription.disable", Reference: "ref_1", Status: "cancelled",
	}); err != nil {
		t.Fatal(err)
	}
	if sub, _ := st.GetSubscription("user_1"); sub.Status != "cancelled" {
		t.Fatalf("second event was swallowed as a duplicate: %+v", sub)
	}
}

// An event we could not resolve must not be recorded as processed, otherwise
// the provider's retry is discarded.
func TestUnresolvedEventIsNotMarkedProcessed(t *testing.T) {
	st := store.NewMemoryStore()
	svc := &service.SubscriptionService{Store: st}

	event := payments.WebhookEvent{
		ID: "charge.success:1", Type: "charge.success", Reference: "ref_late",
		PlanCode: "PLN_x", Status: "active",
	}
	if err := deliver(t, svc, event); !errors.Is(err, service.ErrUnknownSubscription) {
		t.Fatalf("err = %v, want ErrUnknownSubscription", err)
	}

	// The subscription shows up (a delayed write, or an out-of-order delivery)
	// and the retry now succeeds.
	st.PutSubscription(model.Subscription{UserID: "user_1", Status: "pending", Reference: "ref_late", PlanCode: "PLN_x"})
	if err := deliver(t, svc, event); err != nil {
		t.Fatalf("retry after the subscription appeared failed: %v", err)
	}
	if sub, _ := st.GetSubscription("user_1"); sub.Status != "active" {
		t.Fatalf("unexpected %+v", sub)
	}
}

func TestWebhookRejectsInvalidSignatureBeforeApplying(t *testing.T) {
	svc, st := initialized(t)
	svc.Provider = payments.FakeProvider{
		SignatureErr: payments.ErrInvalidSignature,
		Event:        payments.WebhookEvent{ID: "charge.success:1", Reference: "ref_1", Status: "active"},
	}

	err := svc.HandleWebhook(context.Background(), []byte(`{"event":"charge.success"}`), "bad")
	if !errors.Is(err, payments.ErrInvalidSignature) {
		t.Fatalf("err = %v, want ErrInvalidSignature", err)
	}
	if sub, _ := st.GetSubscription("user_1"); sub.Status != "pending" {
		t.Fatalf("an unverified webhook changed state: %+v", sub)
	}
}

// A lifecycle event that carries no plan or reference must not erase the
// correlation data already stored.
func TestWebhookPreservesFieldsTheEventOmits(t *testing.T) {
	svc, st := initialized(t)

	err := deliver(t, svc, payments.WebhookEvent{
		ID: "subscription.disable:5", Type: "subscription.disable",
		UserID: "user_1", Status: "cancelled",
	})
	if err != nil {
		t.Fatal(err)
	}

	sub, _ := st.GetSubscription("user_1")
	if sub.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", sub.Status)
	}
	if sub.Reference != "ref_1" || sub.PlanCode != "PLN_x" {
		t.Fatalf("correlation data was erased: %+v", sub)
	}
}

func TestInitializeValidation(t *testing.T) {
	svc := &service.SubscriptionService{
		Store:    store.NewMemoryStore(),
		Provider: payments.FakeProvider{InitResp: payments.InitializeResponse{Reference: "ref_1", AuthorizationURL: "https://example.test/pay"}},
	}
	cases := []struct {
		name   string
		userID string
		email  string
		amount int64
	}{
		{"missing user", "", "user@example.com", 1000},
		{"blank user", "   ", "user@example.com", 1000},
		{"missing email", "user_1", "", 1000},
		{"malformed email", "user_1", "not-an-email", 1000},
		{"zero amount", "user_1", "user@example.com", 0},
		{"negative amount", "user_1", "user@example.com", -100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Initialize(context.Background(), tc.userID, tc.email, "PLN_x", tc.amount)
			if !errors.Is(err, service.ErrInvalidSubscriptionRequest) {
				t.Fatalf("err = %v, want ErrInvalidSubscriptionRequest", err)
			}
		})
	}
}

// Starting a second checkout must not knock an active subscriber back to
// pending and revoke their access.
func TestInitializeDoesNotDowngradeAnActiveSubscription(t *testing.T) {
	svc, st := initialized(t)
	active, _ := st.GetSubscription("user_1")
	active.Status = "active"
	st.PutSubscription(active)

	svc.Provider = payments.FakeProvider{InitResp: payments.InitializeResponse{Reference: "ref_2", AuthorizationURL: "https://example.test/pay"}}
	if _, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_x", 500000); err != nil {
		t.Fatal(err)
	}

	sub, _ := st.GetSubscription("user_1")
	if sub.Status != "active" {
		t.Fatalf("status = %q, want active", sub.Status)
	}
	if sub.Reference != "ref_2" {
		t.Fatalf("reference = %q, want the new checkout reference", sub.Reference)
	}
	// Both references still resolve, so a callback for either one lands here.
	for _, ref := range []string{"ref_1", "ref_2"} {
		if found, err := st.GetSubscriptionByReference(ref); err != nil || found.UserID != "user_1" {
			t.Fatalf("reference %s no longer resolves: %+v %v", ref, found, err)
		}
	}
}

func TestInitializeProviderFailureIsNotPersisted(t *testing.T) {
	st := store.NewMemoryStore()
	svc := &service.SubscriptionService{Store: st, Provider: payments.FakeProvider{InitErr: payments.ErrProvider}}

	if _, err := svc.Initialize(context.Background(), "user_1", "user@example.com", "PLN_x", 500000); !errors.Is(err, payments.ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
	if _, err := st.GetSubscription("user_1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("a failed initialization left a subscription behind")
	}
}

func TestGetSubscription(t *testing.T) {
	svc, _ := initialized(t)
	if _, err := svc.Get("user_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get("nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
