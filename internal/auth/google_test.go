package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/auth"
)

func userInfo() map[string]string {
	return map[string]string{
		"sub":   "google-123",
		"email": "user@example.com",
		"name":  "Test User",
		"aud":   "client-abc",
		"iss":   "accounts.google.com",
		"exp":   strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10),
	}
}

func tokeninfoServer(t *testing.T, info map[string]string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(info)
	}))
}

func TestVerifyAcceptsValidToken(t *testing.T) {
	srv := tokeninfoServer(t, userInfo(), http.StatusOK)
	defer srv.Close()

	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: srv.URL}
	id, err := v.Verify(context.Background(), "valid-token")
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "google-123" || id.Email != "user@example.com" || id.Name != "Test User" {
		t.Fatalf("unexpected identity %+v", id)
	}
}

func TestVerifyRejectsEmptyToken(t *testing.T) {
	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: "http://irrelevant"}
	if _, err := v.Verify(context.Background(), "   "); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRejectsMismatchedAudience(t *testing.T) {
	info := userInfo()
	info["aud"] = "other-client"
	srv := tokeninfoServer(t, info, http.StatusOK)
	defer srv.Close()

	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: srv.URL}
	if _, err := v.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRejectsWrongIssuer(t *testing.T) {
	info := userInfo()
	info["iss"] = "evil.example.com"
	srv := tokeninfoServer(t, info, http.StatusOK)
	defer srv.Close()

	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: srv.URL}
	if _, err := v.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRejectsExpiredToken(t *testing.T) {
	info := userInfo()
	info["exp"] = strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	srv := tokeninfoServer(t, info, http.StatusOK)
	defer srv.Close()

	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: srv.URL}
	if _, err := v.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRejectsMissingSubject(t *testing.T) {
	info := userInfo()
	delete(info, "sub")
	srv := tokeninfoServer(t, info, http.StatusOK)
	defer srv.Close()

	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: srv.URL}
	if _, err := v.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyRejectsTokenRejectedByProvider(t *testing.T) {
	srv := tokeninfoServer(t, map[string]string{"error_description": "Invalid Value"}, http.StatusBadRequest)
	defer srv.Close()

	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: srv.URL}
	if _, err := v.Verify(context.Background(), "garbage"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyMappingProviderUnavailable(t *testing.T) {
	v := &auth.GoogleVerifier{ClientID: "client-abc", TokenInfoURL: "http://127.0.0.1:1/tokeninfo"}
	if _, err := v.Verify(context.Background(), "token"); !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("expected ErrProviderUnavailable, got %v", err)
	}
}
