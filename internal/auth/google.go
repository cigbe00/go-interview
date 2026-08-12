package auth

import (
	"context"
	"errors"
	"net/http"
)

var ErrInvalidToken = errors.New("invalid identity token")
var ErrProviderUnavailable = errors.New("identity provider unavailable")
var ErrNotImplemented = errors.New("google token verification not implemented")

type Identity struct {
	Subject string
	Email   string
	Name    string
}
type TokenVerifier interface {
	Verify(context.Context, string) (Identity, error)
}

type GoogleVerifier struct {
	ClientID     string
	TokenInfoURL string
	HTTPClient   *http.Client
}

// Verify is intentionally incomplete. See README Task 3.
func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	return Identity{}, ErrNotImplemented
}
