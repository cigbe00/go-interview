package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

type recordingProvider struct {
	body []byte
	sig  string
	err  error
}

func (r *recordingProvider) Initialize(context.Context, payments.InitializeRequest) (payments.InitializeResponse, error) {
	return payments.InitializeResponse{}, nil
}
func (r *recordingProvider) VerifyWebhookSignature(body []byte, sig string) error {
	r.body = append([]byte(nil), body...)
	r.sig = sig
	return r.err
}
func (r *recordingProvider) ParseWebhook([]byte) (payments.WebhookEvent, error) {
	return payments.WebhookEvent{ID: "charge.success:ref_1", UserID: "user_1", Status: "active"}, nil
}

func newTestServer(p payments.Provider) *api.Server {
	st := store.NewMemoryStore()
	return api.New(
		&service.BusinessService{Store: st},
		&service.AuthService{Store: st, Verifier: auth.FakeVerifier{Identity: auth.Identity{Subject: "s", Email: "e@example.com"}}},
		&service.SubscriptionService{Store: st, Provider: p},
		func() bool { return true },
	)
}

func TestCreateReviewRejectsOversizedBody(t *testing.T) {
	srv := newTestServer(&recordingProvider{})
	body := `{"user_id":"u","rating":5,"body":"` + strings.Repeat("a", (1<<20)+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/businesses/biz_1/reviews", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestWebhookPreservesRawBodyWithoutContentLength(t *testing.T) {
	p := &recordingProvider{}
	srv := newTestServer(p)

	payload := `{"event":"charge.success","data":{"reference":"ref_1"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhook", strings.NewReader(payload))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("x-paystack-signature", "abc")

	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if string(p.body) != payload {
		t.Fatalf("provider received body %q, want %q", p.body, payload)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	p := &recordingProvider{err: payments.ErrInvalidSignature}
	srv := newTestServer(p)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions/webhook", strings.NewReader("{}"))
	req.Header.Set("x-paystack-signature", "bad")

	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestCreateReviewReturnsCreated(t *testing.T) {
	srv := newTestServer(&recordingProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/businesses/biz_1/reviews", strings.NewReader(fmt.Sprintf(`{"user_id":"u","rating":5,"body":"Nice"}`)))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %q)", rec.Code, rec.Body.String())
	}
}
