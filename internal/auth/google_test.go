package auth_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/auth"
)

func TestGoogleVerifierAcceptsValidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id_token") != "valid-token" {
			t.Errorf("id_token=%q", r.URL.Query().Get("id_token"))
		}
		fmt.Fprintf(w, `{"aud":"client-1","iss":"https://accounts.google.com","exp":"%d","sub":"google-1","email":"USER@example.com","email_verified":"true","name":"Test User"}`, time.Now().Add(time.Hour).Unix())
	}))
	defer server.Close()

	verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
	identity, err := verifier.Verify(context.Background(), "valid-token")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "google-1" || identity.Email != "user@example.com" || identity.Name != "Test User" {
		t.Fatalf("identity=%+v", identity)
	}
}

func TestGoogleVerifierRejectsInvalidClaims(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"audience", `{"aud":"other","iss":"accounts.google.com","exp":"4102444800","sub":"s","email":"a@example.com","email_verified":"true"}`},
		{"issuer", `{"aud":"client-1","iss":"evil.example","exp":"4102444800","sub":"s","email":"a@example.com","email_verified":"true"}`},
		{"expired", `{"aud":"client-1","iss":"accounts.google.com","exp":"1","sub":"s","email":"a@example.com","email_verified":"true"}`},
		{"subject", `{"aud":"client-1","iss":"accounts.google.com","exp":"4102444800","email":"a@example.com","email_verified":"true"}`},
		{"email", `{"aud":"client-1","iss":"accounts.google.com","exp":"4102444800","sub":"s","email_verified":"true"}`},
		{"unverified", `{"aud":"client-1","iss":"accounts.google.com","exp":"4102444800","sub":"s","email":"a@example.com","email_verified":"false"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tt.body)) }))
			defer server.Close()
			verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
			if _, err := verifier.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("err=%v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestGoogleVerifierClassifiesProviderFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, `{}`},
		{"rate limited", http.StatusTooManyRequests, `{}`},
		{"malformed", http.StatusOK, `{`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
			if _, err := verifier.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrProviderUnavailable) {
				t.Fatalf("err=%v, want ErrProviderUnavailable", err)
			}
		})
	}
}

func TestGoogleVerifierRejectsProviderClientErrorAsInvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }))
	defer server.Close()
	verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
	if _, err := verifier.Verify(context.Background(), "bad-token"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err=%v, want ErrInvalidToken", err)
	}
}

func TestGoogleVerifierRespectsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
	if _, err := verifier.Verify(ctx, "token"); !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err=%v, want ErrProviderUnavailable", err)
	}
}
