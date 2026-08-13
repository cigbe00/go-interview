package service

import (
	"context"
	"errors"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/model"
	"github.com/maoni/backend-takehome/internal/store"
	"strings"
)

var ErrEmailMissing = errors.New("verified identity did not include email")
var ErrSubjectMissing = errors.New("verified identity did not include subject")

type AuthService struct {
	Store    *store.MemoryStore
	Verifier auth.TokenVerifier
}

func (s *AuthService) SignInGoogle(ctx context.Context, token string) (model.User, error) {
	if strings.TrimSpace(token) == "" {
		return model.User{}, auth.ErrInvalidToken
	}
	id, err := s.Verifier.Verify(ctx, token)
	if err != nil {
		return model.User{}, err
	}
	if strings.TrimSpace(id.Subject) == "" {
		return model.User{}, ErrSubjectMissing
	}
	if strings.TrimSpace(id.Email) == "" {
		return model.User{}, ErrEmailMissing
	}
	return s.Store.UpsertUser(model.User{Email: id.Email, Name: id.Name, GoogleID: id.Subject}), nil
}
