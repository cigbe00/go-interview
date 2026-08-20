package service_test

import (
	"context"
	"errors"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
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

func TestAuthServiceRejectsConflictingGoogleSubjectForEmail(t *testing.T) {
	st := store.NewMemoryStore()
	svc := service.AuthService{Store: st, Verifier: auth.FakeVerifier{Identity: auth.Identity{Subject: "google-1", Email: "user@example.com"}}}
	if _, err := svc.SignInGoogle(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
	svc.Verifier = auth.FakeVerifier{Identity: auth.Identity{Subject: "google-2", Email: "USER@example.com"}}
	if _, err := svc.SignInGoogle(context.Background(), "token"); !errors.Is(err, store.ErrIdentityConflict) {
		t.Fatalf("err=%v, want ErrIdentityConflict", err)
	}
}
