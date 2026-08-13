package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/auth"
)

// A compromised or misbehaving identity provider can return anything at all.
// Verify must never panic on the response, and — whatever the bytes — must
// never mint an identity that fails the checks it exists to enforce.
func FuzzVerifyTokenInfoResponse(f *testing.F) {
	seeds := []string{
		`{"iss":"https://accounts.google.com","aud":"` + testClientID + `","sub":"1","email":"a@b.c","email_verified":"true","exp":"99999999999"}`,
		`{"iss":"accounts.google.com","aud":"` + testClientID + `","sub":"1","email":"a@b.c","email_verified":true,"exp":99999999999}`,
		`{"error":"invalid_token"}`,
		`{"exp":"not-a-number"}`,
		`{"exp":-1,"email_verified":"maybe"}`,
		`{"sub":null,"email":null}`,
		`{"aud":["one","two"]}`,
		`{}`,
		``,
		`null`,
		`[1,2,3]`,
		`{"email_verified":"TRUE","exp":"0"}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, response []byte) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(response)
		}))
		defer srv.Close()

		v := &auth.GoogleVerifier{
			ClientID:     testClientID,
			TokenInfoURL: srv.URL,
			HTTPClient:   &http.Client{Timeout: 2 * time.Second},
		}

		identity, err := v.Verify(context.Background(), "token")
		if err != nil {
			// Every rejection must be classified, so the API can tell a bad
			// credential (401) from a provider problem (502).
			if !errors.Is(err, auth.ErrInvalidToken) && !errors.Is(err, auth.ErrProviderUnavailable) {
				t.Fatalf("unclassified verification error %v for response %q", err, response)
			}
			return
		}

		// An accepted identity must satisfy every invariant the rest of the
		// system relies on: a stable subject to key the account on, and an
		// email to attach to it.
		if identity.Subject == "" {
			t.Fatalf("accepted an identity with no subject: %q", response)
		}
		if identity.Email == "" {
			t.Fatalf("accepted an identity with no email: %q", response)
		}
	})
}
