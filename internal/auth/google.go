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
	tokenURL, err := url.Parse(g.TokenInfoURL)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: invalid token-info URL", ErrProviderUnavailable)
	}
	query := tokenURL.Query()
	query.Set("id_token", idToken)
	tokenURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: create token-info request", ErrProviderUnavailable)
	}
	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: token-info request failed: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Identity{}, fmt.Errorf("%w: token-info returned %s", ErrProviderUnavailable, resp.Status)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Identity{}, ErrInvalidToken
	}

	var claims struct {
		Audience      string          `json:"aud"`
		Issuer        string          `json:"iss"`
		ExpiresAt     json.RawMessage `json:"exp"`
		Subject       string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
		Error         string          `json:"error_description"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&claims); err != nil {
		return Identity{}, fmt.Errorf("%w: decode token-info response", ErrProviderUnavailable)
	}
	if claims.Error != "" || claims.Audience != g.ClientID {
		return Identity{}, ErrInvalidToken
	}
	if claims.Issuer != "accounts.google.com" && claims.Issuer != "https://accounts.google.com" {
		return Identity{}, ErrInvalidToken
	}
	expiresAt, err := parseUnixClaim(claims.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) {
		return Identity{}, ErrInvalidToken
	}
	verified, err := parseBoolClaim(claims.EmailVerified)
	if err != nil || !verified || strings.TrimSpace(claims.Subject) == "" || strings.TrimSpace(claims.Email) == "" {
		return Identity{}, ErrInvalidToken
	}
	return Identity{
		Subject: strings.TrimSpace(claims.Subject),
		Email:   strings.ToLower(strings.TrimSpace(claims.Email)),
		Name:    strings.TrimSpace(claims.Name),
	}, nil
}

func parseUnixClaim(raw json.RawMessage) (time.Time, error) {
	value := strings.Trim(string(raw), `"`)
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(seconds, 0), nil
}

func parseBoolClaim(raw json.RawMessage) (bool, error) {
	value := strings.Trim(string(raw), `"`)
	return strconv.ParseBool(value)
}
