package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/auth"
)

const testClientID = "test-client-id.apps.googleusercontent.com"

// validClaims is a tokeninfo response for a healthy token. The endpoint
// returns exp and email_verified as strings, which is what production sends.
func validClaims() map[string]any {
	return map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            testClientID,
		"sub":            "108412345678901234567",
		"email":          "user@example.com",
		"email_verified": "true",
		"exp":            fmt.Sprint(time.Now().Add(time.Hour).Unix()),
		"name":           "Ada Lovelace",
	}
}

// tokenInfoServer serves a fixed status and body, and records the token it was
// asked about.
func tokenInfoServer(t *testing.T, status int, body any) (*httptest.Server, *string) {
	t.Helper()
	var seenToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenToken = r.URL.Query().Get("id_token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		switch v := body.(type) {
		case string:
			_, _ = w.Write([]byte(v))
		default:
			_ = json.NewEncoder(w).Encode(v)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seenToken
}

func verifierFor(srv *httptest.Server) *auth.GoogleVerifier {
	return &auth.GoogleVerifier{
		ClientID:     testClientID,
		TokenInfoURL: srv.URL + "/tokeninfo",
		HTTPClient:   &http.Client{Timeout: 2 * time.Second},
	}
}

func TestVerifyReturnsNormalizedIdentity(t *testing.T) {
	srv, seen := tokenInfoServer(t, http.StatusOK, validClaims())

	id, err := verifierFor(srv).Verify(context.Background(), "the-id-token")
	if err != nil {
		t.Fatal(err)
	}
	if *seen != "the-id-token" {
		t.Fatalf("provider received id_token %q", *seen)
	}
	if id.Subject != "108412345678901234567" {
		t.Fatalf("subject = %q; the stable google sub must be preserved", id.Subject)
	}
	if id.Email != "user@example.com" || id.Name != "Ada Lovelace" {
		t.Fatalf("unexpected identity %+v", id)
	}
}

// The decoded JWT payload types numeric and boolean claims natively; both
// spellings have to decode.
func TestVerifyAcceptsNativelyTypedClaims(t *testing.T) {
	claims := validClaims()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	claims["email_verified"] = true
	srv, _ := tokenInfoServer(t, http.StatusOK, claims)

	if _, err := verifierFor(srv).Verify(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsBadTokens(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"audience minted for another app", func(c map[string]any) { c["aud"] = "someone-else.apps.googleusercontent.com" }},
		{"missing audience", func(c map[string]any) { delete(c, "aud") }},
		{"unexpected issuer", func(c map[string]any) { c["iss"] = "https://evil.example.com" }},
		{"missing issuer", func(c map[string]any) { delete(c, "iss") }},
		{"expired", func(c map[string]any) { c["exp"] = fmt.Sprint(time.Now().Add(-time.Hour).Unix()) }},
		{"missing expiry", func(c map[string]any) { delete(c, "exp") }},
		{"missing subject", func(c map[string]any) { delete(c, "sub") }},
		{"missing email", func(c map[string]any) { delete(c, "email") }},
		{"unverified email", func(c map[string]any) { c["email_verified"] = "false" }},
		{"provider error field", func(c map[string]any) { c["error"] = "invalid_token" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := validClaims()
			tc.mutate(claims)
			srv, _ := tokenInfoServer(t, http.StatusOK, claims)

			_, err := verifierFor(srv).Verify(context.Background(), "tok")
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("err = %v, want ErrInvalidToken", err)
			}
			if errors.Is(err, auth.ErrProviderUnavailable) {
				t.Fatal("a bad token must not be reported as a provider outage")
			}
		})
	}
}

func TestVerifyRejectsEmptyTokenWithoutCallingProvider(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer srv.Close()

	for _, token := range []string{"", "   "} {
		if _, err := verifierFor(srv).Verify(context.Background(), token); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("token %q: err = %v, want ErrInvalidToken", token, err)
		}
	}
	if called {
		t.Fatal("an empty token should not reach the provider")
	}
}

// Google answers 400/401 for a malformed or expired token: that is a rejected
// credential, not an outage.
func TestVerifyMapsProviderRejectionToInvalidToken(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		srv, _ := tokenInfoServer(t, status, map[string]any{"error": "invalid_token"})
		_, err := verifierFor(srv).Verify(context.Background(), "tok")
		if !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("status %d: err = %v, want ErrInvalidToken", status, err)
		}
	}
}

// Anything that means "we could not get an answer" must be distinguishable, so
// the API can answer 502 instead of telling a legitimate user their
// credentials were rejected.
func TestVerifyMapsProviderFailuresToUnavailable(t *testing.T) {
	t.Run("server error", func(t *testing.T) {
		srv, _ := tokenInfoServer(t, http.StatusInternalServerError, map[string]any{})
		if _, err := verifierFor(srv).Verify(context.Background(), "tok"); !errors.Is(err, auth.ErrProviderUnavailable) {
			t.Fatalf("err = %v, want ErrProviderUnavailable", err)
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		srv, _ := tokenInfoServer(t, http.StatusTooManyRequests, map[string]any{})
		if _, err := verifierFor(srv).Verify(context.Background(), "tok"); !errors.Is(err, auth.ErrProviderUnavailable) {
			t.Fatalf("err = %v, want ErrProviderUnavailable", err)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		srv, _ := tokenInfoServer(t, http.StatusOK, "{not json")
		if _, err := verifierFor(srv).Verify(context.Background(), "tok"); !errors.Is(err, auth.ErrProviderUnavailable) {
			t.Fatalf("err = %v, want ErrProviderUnavailable", err)
		}
	})

	t.Run("unreachable host", func(t *testing.T) {
		srv, _ := tokenInfoServer(t, http.StatusOK, validClaims())
		v := verifierFor(srv)
		srv.Close() // nothing is listening any more
		if _, err := v.Verify(context.Background(), "tok"); !errors.Is(err, auth.ErrProviderUnavailable) {
			t.Fatalf("err = %v, want ErrProviderUnavailable", err)
		}
	})
}

// A missing client ID is a deployment fault. Verifying against an empty
// audience would accept tokens minted for any application, so it must fail
// closed — and as an outage, not as a bad credential.
func TestVerifyFailsClosedWhenClientIDIsNotConfigured(t *testing.T) {
	srv, _ := tokenInfoServer(t, http.StatusOK, validClaims())
	v := verifierFor(srv)
	v.ClientID = ""

	_, err := v.Verify(context.Background(), "tok")
	if !errors.Is(err, auth.ErrNotConfigured) || !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestVerifyRespectsRequestContext(t *testing.T) {
	// release guarantees the handler returns even if the server never notices
	// the client going away, so shutting the test server down cannot block.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done(): // the caller gave up
		case <-release:
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := verifierFor(srv).Verify(ctx, "tok")
	if !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("call did not honour the context deadline (took %s)", elapsed)
	}
}

func TestVerifyRejectsUnusableTokenInfoURL(t *testing.T) {
	v := &auth.GoogleVerifier{ClientID: testClientID, TokenInfoURL: "not-a-url"}
	if _, err := v.Verify(context.Background(), "tok"); !errors.Is(err, auth.ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
}

// The configured URL may already carry query parameters; adding id_token must
// not drop them.
func TestVerifyPreservesConfiguredQueryParameters(t *testing.T) {
	var gotAlt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAlt = r.URL.Query().Get("alt")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(validClaims())
	}))
	defer srv.Close()

	v := verifierFor(srv)
	v.TokenInfoURL = srv.URL + "/tokeninfo?alt=json"
	if _, err := v.Verify(context.Background(), "tok"); err != nil {
		t.Fatal(err)
	}
	if gotAlt != "json" {
		t.Fatalf("configured query parameter was dropped (alt=%q)", gotAlt)
	}
}
