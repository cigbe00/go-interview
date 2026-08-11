package auth

import (
	"context"
	"errors"
)

var ErrInvalidToken = errors.New("invalid identity token")
var ErrNotImplemented = errors.New("google token verification not implemented")

type Identity struct {
	Subject string
	Email   string
	Name    string
}

type TokenVerifier interface {
	Verify(ctx context.Context, idToken string) (Identity, error)
}

// GoogleVerifier is intentionally incomplete for the exercise.
// Implement production-quality token verification without hard-coded secrets.
type GoogleVerifier struct {
	ClientID     string
	TokenInfoURL string
}

func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	return Identity{}, ErrNotImplemented
}
