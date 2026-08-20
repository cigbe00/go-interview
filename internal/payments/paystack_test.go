package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maoni/backend-takehome/internal/payments"
)

func TestPaystackInitializeSendsAuthenticatedCorrelatedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/transaction/initialize" {
			t.Errorf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers=%v", r.Header)
		}
		var request struct {
			Email     string `json:"email"`
			Amount    int64  `json:"amount"`
			Plan      string `json:"plan"`
			Reference string `json:"reference"`
			Metadata  struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Email != "user@example.com" || request.Amount != 500000 || request.Plan != "PLN_1" || request.Metadata.UserID != "user_1" {
			t.Errorf("request=%+v", request)
		}
		fmt.Fprintf(w, `{"status":true,"message":"ok","data":{"authorization_url":"https://pay.test/authorize","access_code":"code","reference":%q}}`, request.Reference)
	}))
	defer server.Close()

	client := payments.PaystackClient{SecretKey: "secret", BaseURL: server.URL, HTTPClient: server.Client()}
	response, err := client.Initialize(context.Background(), payments.InitializeRequest{
		UserID: "user_1", Email: "user@example.com", Amount: 500000, PlanCode: "PLN_1", Reference: "ref_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Reference != "ref_1" || response.AccessCode != "code" {
		t.Fatalf("response=%+v", response)
	}
}

func TestPaystackInitializeRejectsProviderFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"http error", http.StatusBadGateway, `{}`},
		{"declared failure", http.StatusOK, `{"status":false,"message":"bad"}`},
		{"malformed", http.StatusOK, `{`},
		{"missing data", http.StatusOK, `{"status":true,"data":{}}`},
		{"reference mismatch", http.StatusOK, `{"status":true,"data":{"authorization_url":"https://pay.test","access_code":"code","reference":"wrong"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := payments.PaystackClient{SecretKey: "secret", BaseURL: server.URL, HTTPClient: server.Client()}
			_, err := client.Initialize(context.Background(), payments.InitializeRequest{Reference: "ref_1"})
			if !errors.Is(err, payments.ErrProvider) {
				t.Fatalf("err=%v, want ErrProvider", err)
			}
		})
	}
}

func TestPaystackInitializeRespectsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := payments.PaystackClient{SecretKey: "secret", BaseURL: server.URL, HTTPClient: server.Client()}
	_, err := client.Initialize(ctx, payments.InitializeRequest{Reference: "ref_1"})
	if !errors.Is(err, payments.ErrProvider) {
		t.Fatalf("err=%v, want ErrProvider", err)
	}
}

func TestPaystackWebhookSignature(t *testing.T) {
	body := []byte(`{"event":"charge.success"}`)
	mac := hmac.New(sha512.New, []byte("secret"))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))
	client := payments.PaystackClient{SecretKey: "secret"}
	if err := client.VerifyWebhookSignature(body, signature); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "not-hex", hex.EncodeToString(make([]byte, sha512.Size))} {
		if err := client.VerifyWebhookSignature(body, invalid); !errors.Is(err, payments.ErrInvalidSignature) {
			t.Fatalf("signature=%q err=%v", invalid, err)
		}
	}
}

func TestPaystackParseWebhook(t *testing.T) {
	client := payments.PaystackClient{}
	event, err := client.ParseWebhook([]byte(`{
		"event":"charge.success",
		"data":{"id":42,"reference":"ref_1","status":"success","customer":{"email":"USER@example.com"},"plan":{"plan_code":"PLN_1"},"metadata":{"user_id":"user_1"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "charge.success:42" || event.UserID != "user_1" || event.Reference != "ref_1" || event.Status != "active" || event.Email != "user@example.com" {
		t.Fatalf("event=%+v", event)
	}
	unsupported, err := client.ParseWebhook([]byte(`{"event":"customeridentification.failed","data":{}}`))
	if err != nil || unsupported.Type != "customeridentification.failed" {
		t.Fatalf("event=%+v err=%v", unsupported, err)
	}
	if _, err := client.ParseWebhook([]byte(`{"event":"charge.success","data":{}}`)); !errors.Is(err, payments.ErrInvalidWebhook) {
		t.Fatalf("err=%v, want ErrInvalidWebhook", err)
	}
}
