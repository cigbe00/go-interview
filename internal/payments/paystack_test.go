package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maoni/backend-takehome/internal/payments"
)

func signature(body []byte, secret string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestInitializeSendsAuthAndMetadata(t *testing.T) {
	var gotAuth, gotUserID, gotPlan string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transaction/initialize" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Plan     string            `json:"plan"`
			Metadata map[string]string `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotPlan = body.Plan
		gotUserID = body.Metadata["user_id"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": true,
			"data": map[string]any{
				"authorization_url": "https://checkout.paystack.com/abc",
				"access_code":       "code_1",
				"reference":         "ref_1",
			},
		})
	}))
	defer srv.Close()

	c := &payments.PaystackClient{SecretKey: "sk_test", BaseURL: srv.URL}
	resp, err := c.Initialize(context.Background(), payments.InitializeRequest{
		UserID: "user_1", Email: "user@example.com", Amount: 500000, PlanCode: "PLN_1", Reference: "ref_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.AuthorizationURL == "" || resp.Reference != "ref_1" || resp.AccessCode != "code_1" {
		t.Fatalf("unexpected response %+v", resp)
	}
	if gotAuth != "Bearer sk_test" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotPlan != "PLN_1" || gotUserID != "user_1" {
		t.Fatalf("plan = %q, user_id = %q", gotPlan, gotUserID)
	}
}

func TestInitializeProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": false, "message": "bad request"})
	}))
	defer srv.Close()

	c := &payments.PaystackClient{SecretKey: "sk_test", BaseURL: srv.URL}
	if _, err := c.Initialize(context.Background(), payments.InitializeRequest{UserID: "u", Email: "e@e.com", Reference: "r"}); !errors.Is(err, payments.ErrProvider) {
		t.Fatalf("expected ErrProvider, got %v", err)
	}
}

func TestInitializeMalformedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := &payments.PaystackClient{SecretKey: "sk_test", BaseURL: srv.URL}
	if _, err := c.Initialize(context.Background(), payments.InitializeRequest{UserID: "u", Email: "e@e.com", Reference: "r"}); !errors.Is(err, payments.ErrProvider) {
		t.Fatalf("expected ErrProvider, got %v", err)
	}
}

func TestVerifyWebhookSignature(t *testing.T) {
	c := &payments.PaystackClient{SecretKey: "sk_test"}
	body := []byte(`{"event":"charge.success","data":{"reference":"ref_1"}}`)

	if err := c.VerifyWebhookSignature(body, signature(body, "sk_test")); err != nil {
		t.Fatalf("expected valid signature, got %v", err)
	}
	if err := c.VerifyWebhookSignature(body, signature(body, "wrong")); !errors.Is(err, payments.ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
	if err := c.VerifyWebhookSignature(body, ""); !errors.Is(err, payments.ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for empty header, got %v", err)
	}
}

func TestParseWebhookChargeSuccess(t *testing.T) {
	c := &payments.PaystackClient{}
	body := []byte(`{
		"event": "charge.success",
		"data": {
			"id": 123456,
			"reference": "ref_1",
			"plan": {"plan_code": "PLN_1"},
			"metadata": {"user_id": "user_99"},
			"customer": {"email": "buyer@example.com"}
		}
	}`)
	ev, err := c.ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID != "charge.success:ref_1" || ev.UserID != "user_99" || ev.PlanCode != "PLN_1" || ev.Status != "active" {
		t.Fatalf("unexpected event %+v", ev)
	}
}

func TestParseWebhookUnsupportedEvent(t *testing.T) {
	c := &payments.PaystackClient{}
	if _, err := c.ParseWebhook([]byte(`{"event":"transfer.failed","data":{}}`)); err == nil {
		t.Fatal("expected error for unsupported event")
	}
}

func TestParseWebhookMissingIdentity(t *testing.T) {
	c := &payments.PaystackClient{}
	if _, err := c.ParseWebhook([]byte(`{"event":"charge.success","data":{}}`)); err == nil {
		t.Fatal("expected error when data has no identity")
	}
}
