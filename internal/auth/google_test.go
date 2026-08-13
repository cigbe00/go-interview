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

func TestGoogleVerifierValidatesAndNormalizesIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id_token") != "valid-token" {
			t.Fatal("token was not sent as a query parameter")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"aud":"client-1","iss":"https://accounts.google.com","exp":"%d","sub":"google-123","email":" USER@Example.COM ","email_verified":"true","name":"Example User"}`, time.Now().Add(time.Minute).Unix())
	}))
	defer server.Close()

	verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
	identity, err := verifier.Verify(context.Background(), "valid-token")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "google-123" || identity.Email != "user@example.com" {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestGoogleVerifierRejectsInvalidClaims(t *testing.T) {
	tests := []struct {
		name, aud, issuer, expiry, verified string
	}{
		{"wrong audience", "other", "accounts.google.com", fmt.Sprint(time.Now().Add(time.Minute).Unix()), "true"},
		{"wrong issuer", "client-1", "example.com", fmt.Sprint(time.Now().Add(time.Minute).Unix()), "true"},
		{"expired", "client-1", "accounts.google.com", fmt.Sprint(time.Now().Add(-time.Minute).Unix()), "true"},
		{"unverified email", "client-1", "accounts.google.com", fmt.Sprint(time.Now().Add(time.Minute).Unix()), "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprintf(w, `{"aud":%q,"iss":%q,"exp":%q,"sub":"subject","email":"user@example.com","email_verified":%q}`, tt.aud, tt.issuer, tt.expiry, tt.verified)
			}))
			defer server.Close()
			verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
			_, err := verifier.Verify(context.Background(), "token")
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestGoogleVerifierClassifiesProviderFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	verifier := auth.GoogleVerifier{ClientID: "client-1", TokenInfoURL: server.URL, HTTPClient: server.Client()}
	_, err := verifier.Verify(context.Background(), "token")
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("error = %v, want ErrProviderUnavailable", err)
	}
}
