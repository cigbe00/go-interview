package service

import (
	"context"
	"errors"
	"strings"

	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
)

var ErrEmailMissing = errors.New("verified identity did not include email")

type AuthService struct {
	Store    *store.MemoryStore
	Verifier auth.TokenVerifier
}

func (s *AuthService) SignInGoogle(ctx context.Context, token string) (model.User, error) {
	if strings.TrimSpace(token) == "" {
		return model.User{}, auth.ErrInvalidToken
	}
	identity, err := s.Verifier.Verify(ctx, token)
	if err != nil {
		return model.User{}, err
	}
	if identity.Email == "" {
		return model.User{}, ErrEmailMissing
	}
	return s.Store.UpsertUser(model.User{Email: identity.Email, Name: identity.Name, GoogleID: identity.Subject}), nil
}
