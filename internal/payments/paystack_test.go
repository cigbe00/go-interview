package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maoni/backend-takehome/internal/payments"
)

func TestPaystackInitializeSendsAuthenticatedCorrelatedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transaction/initialize" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"user_id":"user_1"`) || !strings.Contains(string(body), `"plan":"PLN_1"`) {
			t.Fatalf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":true,"message":"ok","data":{"authorization_url":"https://pay.example/1","access_code":"code","reference":"ref_1"}}`)
	}))
	defer server.Close()

	client := payments.PaystackClient{SecretKey: "secret", BaseURL: server.URL, HTTPClient: server.Client()}
	got, err := client.Initialize(context.Background(), payments.InitializeRequest{UserID: "user_1", Email: "u@example.com", Amount: 500000, PlanCode: "PLN_1", Reference: "ref_1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reference != "ref_1" || got.AccessCode != "code" {
		t.Fatalf("response = %+v", got)
	}
}

func TestPaystackWebhookSignatureAndParsing(t *testing.T) {
	body := []byte(`{"event":"charge.success","data":{"id":42,"reference":"ref_1","status":"success","customer":{"email":"u@example.com"},"plan":{"plan_code":"PLN_1"},"metadata":{"user_id":"user_1"}}}`)
	mac := hmac.New(sha512.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	client := payments.PaystackClient{SecretKey: "secret"}
	if err := client.VerifyWebhookSignature(body, signature); err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyWebhookSignature(body, "bad"); !errors.Is(err, payments.ErrInvalidSignature) {
		t.Fatalf("error = %v, want ErrInvalidSignature", err)
	}
	event, err := client.ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "charge.success:42" || event.UserID != "user_1" || event.Reference != "ref_1" || event.Status != "active" {
		t.Fatalf("event = %+v", event)
	}
}

func TestPaystackInitializeRejectsProviderFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"non-2xx", http.StatusBadRequest, `{"status":false,"message":"invalid plan"}`},
		{"declared failure", http.StatusOK, `{"status":false,"message":"declined"}`},
		{"malformed response", http.StatusOK, `{`},
		{"missing response fields", http.StatusOK, `{"status":true,"data":{"reference":"ref_1"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()
			client := payments.PaystackClient{SecretKey: "secret", BaseURL: server.URL, HTTPClient: server.Client()}
			_, err := client.Initialize(context.Background(), payments.InitializeRequest{UserID: "user_1", Email: "u@example.com", Amount: 1, PlanCode: "PLN_1", Reference: "ref_1"})
			if !errors.Is(err, payments.ErrProvider) {
				t.Fatalf("error = %v, want ErrProvider", err)
			}
		})
	}
}
