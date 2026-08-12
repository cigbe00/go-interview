package service_test

import (
	"context"
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
