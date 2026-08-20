package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	cachepkg "github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

func newTestServer(verifier auth.TokenVerifier, provider payments.Provider) *api.Server {
	st := store.NewMemoryStore()
	if verifier == nil {
		verifier = auth.FakeVerifier{Identity: auth.Identity{Subject: "google-1", Email: "user@example.com"}}
	}
	if provider == nil {
		provider = payments.FakeProvider{}
	}
	return api.New(
		&service.BusinessService{Store: st, Cache: cachepkg.NewMemoryBusinessCache()},
		&service.AuthService{Store: st, Verifier: verifier},
		&service.SubscriptionService{Store: st, Provider: provider},
		nil,
	)
}

func performRequest(server *api.Server, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.Echo.ServeHTTP(recorder, request)
	return recorder
}

func TestBusinessAndReviewEndpointsUseDocumentedIDs(t *testing.T) {
	server := newTestServer(nil, nil)
	response := performRequest(server, http.MethodGet, "/api/v1/businesses/biz_1", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var business struct {
		ReviewCount int     `json:"review_count"`
		Average     float64 `json:"average_rating"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &business); err != nil {
		t.Fatal(err)
	}
	if business.ReviewCount != 3 || business.Average <= 4.3 {
		t.Fatalf("business=%+v", business)
	}

	response = performRequest(server, http.MethodGet, "/api/v1/businesses/missing/reviews", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestHandlersRejectUnknownAndTrailingJSON(t *testing.T) {
	server := newTestServer(nil, nil)
	for _, body := range []string{
		`{"user_id":"user_1","rating":5,"unknown":true}`,
		`{"user_id":"user_1","rating":5}{"rating":4}`,
	} {
		response := performRequest(server, http.MethodPost, "/api/v1/businesses/biz_1/reviews", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, response.Code, response.Body.String())
		}
	}
}

func TestGoogleAuthMapsProviderFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid", auth.ErrInvalidToken, http.StatusUnauthorized},
		{"provider", auth.ErrProviderUnavailable, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(auth.FakeVerifier{Err: tt.err}, nil)
			response := performRequest(server, http.MethodPost, "/api/v1/auth/google", `{"id_token":"token"}`)
			if response.Code != tt.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

type webhookProvider struct{}

func (webhookProvider) Initialize(context.Context, payments.InitializeRequest) (payments.InitializeResponse, error) {
	return payments.InitializeResponse{}, errors.New("not used")
}
func (webhookProvider) VerifyWebhookSignature([]byte, string) error { return nil }
func (webhookProvider) ParseWebhook([]byte) (payments.WebhookEvent, error) {
	return payments.WebhookEvent{Type: "ignored"}, nil
}

func TestWebhookReadsChunkedBodyAndEnforcesLimit(t *testing.T) {
	server := newTestServer(nil, webhookProvider{})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhook", bytes.NewBufferString(`{"event":"ignored"}`))
	request.ContentLength = -1
	recorder := httptest.NewRecorder()
	server.Echo.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	oversized := strings.Repeat("x", (1<<20)+1)
	response := performRequest(server, http.MethodPost, "/api/v1/subscriptions/webhook", oversized)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
