package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

func TestAuthServiceAcceptsVerifiedIdentity(t *testing.T) {
	svc := service.AuthService{Store: store.NewMemoryStore(), Verifier: auth.FakeVerifier{Identity: auth.Identity{Subject: "google-1", Email: "user@example.com", Name: "Test User"}}}
	u, err := svc.SignInGoogle(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if u.GoogleID != "google-1" {
		t.Fatalf("unexpected user %+v", u)
	}
}

func TestSignInRejectsEmptyTokenWithoutVerifying(t *testing.T) {
	verifier := &countingVerifier{identity: auth.Identity{Subject: "google-1", Email: "user@example.com"}}
	svc := service.AuthService{Store: store.NewMemoryStore(), Verifier: verifier}

	for _, token := range []string{"", "   "} {
		if _, err := svc.SignInGoogle(context.Background(), token); !errors.Is(err, auth.ErrInvalidToken) {
			t.Fatalf("token %q: err = %v, want ErrInvalidToken", token, err)
		}
	}
	if verifier.calls != 0 {
		t.Fatalf("verifier was called %d times for empty tokens", verifier.calls)
	}
}

func TestSignInPropagatesVerifierErrors(t *testing.T) {
	for _, want := range []error{auth.ErrInvalidToken, auth.ErrProviderUnavailable} {
		svc := service.AuthService{Store: store.NewMemoryStore(), Verifier: auth.FakeVerifier{Err: want}}
		if _, err := svc.SignInGoogle(context.Background(), "token"); !errors.Is(err, want) {
			t.Fatalf("err = %v, want %v", err, want)
		}
	}
}

func TestSignInRejectsIncompleteIdentities(t *testing.T) {
	cases := []struct {
		name     string
		identity auth.Identity
		want     error
	}{
		{"no subject", auth.Identity{Email: "user@example.com"}, service.ErrSubjectMissing},
		{"no email", auth.Identity{Subject: "google-1"}, service.ErrEmailMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := service.AuthService{Store: store.NewMemoryStore(), Verifier: auth.FakeVerifier{Identity: tc.identity}}
			if _, err := svc.SignInGoogle(context.Background(), "token"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// The Google subject is the account key, so a user who changes their email
// keeps the same local account instead of getting a second one.
func TestRepeatSignInKeepsTheSameAccountAcrossEmailChange(t *testing.T) {
	st := store.NewMemoryStore()
	svc := service.AuthService{Store: st}

	svc.Verifier = auth.FakeVerifier{Identity: auth.Identity{Subject: "google-1", Email: "old@example.com", Name: "User"}}
	first, err := svc.SignInGoogle(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}

	svc.Verifier = auth.FakeVerifier{Identity: auth.Identity{Subject: "google-1", Email: "new@example.com", Name: "User"}}
	second, err := svc.SignInGoogle(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("a changed email created a second account: %s vs %s", second.ID, first.ID)
	}
	if second.Email != "new@example.com" {
		t.Fatalf("email was not updated: %+v", second)
	}
}

type countingVerifier struct {
	identity auth.Identity
	calls    int
}

func (c *countingVerifier) Verify(context.Context, string) (auth.Identity, error) {
	c.calls++
	return c.identity, nil
}
