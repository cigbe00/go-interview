package service_test

import (
	"context"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
	"testing"
)

func TestGoogleSignInUsesVerifier(t *testing.T) {
	svc := &service.AuthService{Store: store.NewMemoryStore(), Verifier: auth.FakeVerifier{Identity: auth.Identity{Subject: "g123", Email: "dev@example.com", Name: "Dev"}}}
	u, err := svc.SignInGoogle(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "dev@example.com" {
		t.Fatalf("unexpected user: %+v", u)
	}
}
