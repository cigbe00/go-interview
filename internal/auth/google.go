package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrInvalidToken = errors.New("invalid identity token")
var ErrProviderUnavailable = errors.New("identity provider unavailable")

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

func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return Identity{}, ErrInvalidToken
	}
	if strings.TrimSpace(g.ClientID) == "" || strings.TrimSpace(g.TokenInfoURL) == "" {
		return Identity{}, fmt.Errorf("%w: google verifier is not configured", ErrProviderUnavailable)
	}
	u, err := url.Parse(g.TokenInfoURL)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: invalid token-info URL", ErrProviderUnavailable)
	}
	q := u.Query()
	q.Set("id_token", idToken)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: create request", ErrProviderUnavailable)
	}
	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return Identity{}, fmt.Errorf("%w: token-info returned %s", ErrProviderUnavailable, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, ErrInvalidToken
	}
	var payload struct {
		Audience      string `json:"aud"`
		Issuer        string `json:"iss"`
		ExpiresAt     string `json:"exp"`
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		Name          string `json:"name"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&payload); err != nil {
		return Identity{}, fmt.Errorf("%w: malformed token-info response", ErrProviderUnavailable)
	}
	exp, err := strconv.ParseInt(payload.ExpiresAt, 10, 64)
	verified := payload.EmailVerified == true || payload.EmailVerified == "true"
	validIssuer := payload.Issuer == "accounts.google.com" || payload.Issuer == "https://accounts.google.com"
	if err != nil || time.Now().Unix() >= exp || payload.Audience != g.ClientID || !validIssuer ||
		strings.TrimSpace(payload.Subject) == "" || strings.TrimSpace(payload.Email) == "" || !verified {
		return Identity{}, ErrInvalidToken
	}
	return Identity{Subject: payload.Subject, Email: strings.ToLower(strings.TrimSpace(payload.Email)), Name: strings.TrimSpace(payload.Name)}, nil
}
