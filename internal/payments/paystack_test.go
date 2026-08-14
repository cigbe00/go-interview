package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maoni/backend-takehome/internal/payments"
)

const testSecretKey = "sk_test_1234567890abcdef"

func computeHMAC(payload []byte, secret string) string {
	h := hmac.New(sha512.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}

func TestPaystackClient_Initialize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testSecretKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": true,
			"message": "Authorization URL created",
			"data": {
				"authorization_url": "https://checkout.paystack.com/access_code",
				"access_code": "access_code_123",
				"reference": "maoni_user_1_100"
			}
		}`))
	}))
	defer server.Close()

	client := payments.NewPaystackClient(testSecretKey, server.URL, server.Client())
	resp, err := client.Initialize(context.Background(), payments.InitializeRequest{
		UserID:    "user_1",
		Email:     "user@example.com",
		Amount:    500000,
		PlanCode:  "PLN_premium",
		Reference: "maoni_user_1_100",
	})

	if err != nil {
		t.Fatalf("unexpected error initializing transaction: %v", err)
	}
	if resp.Reference != "maoni_user_1_100" || resp.AuthorizationURL == "" || resp.AccessCode == "" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestPaystackClient_VerifyWebhookSignature(t *testing.T) {
	client := payments.NewPaystackClient(testSecretKey, "", nil)
	payload := []byte(`{"event":"charge.success","data":{"reference":"ref_123"}}`)

	validSig := computeHMAC(payload, testSecretKey)
	if err := client.VerifyWebhookSignature(payload, validSig); err != nil {
		t.Errorf("expected valid signature, got: %v", err)
	}

	invalidSig := computeHMAC(payload, "wrong_secret")
	if err := client.VerifyWebhookSignature(payload, invalidSig); err == nil {
		t.Errorf("expected error for invalid signature, got nil")
	}
}

func TestPaystackClient_ParseWebhook(t *testing.T) {
	client := payments.NewPaystackClient(testSecretKey, "", nil)
	payload := []byte(`{
		"event": "charge.success",
		"data": {
			"id": 100200300,
			"reference": "maoni_user_1_100",
			"status": "success",
			"customer": {
				"email": "user@example.com"
			},
			"plan": {
				"plan_code": "PLN_premium"
			}
		}
	}`)

	event, err := client.ParseWebhook(payload)
	if err != nil {
		t.Fatalf("unexpected error parsing webhook: %v", err)
	}

	if event.Reference != "maoni_user_1_100" || event.Status != "success" || event.Email != "user@example.com" {
		t.Errorf("unexpected event output: %+v", event)
	}
	if event.Type != "charge.success" {
		t.Errorf("expected Type 'charge.success', got '%s'", event.Type)
	}
}
