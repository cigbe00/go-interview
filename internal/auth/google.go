package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidToken        = errors.New("invalid identity token")
	ErrProviderUnavailable = errors.New("identity provider unavailable")
	ErrNotImplemented      = errors.New("google token verification not implemented")

	ErrInvalidAudience = errors.New("token audience does not match configured client id")
	ErrInvalidIssuer   = errors.New("invalid token issuer")
	ErrUnverifiedEmail = errors.New("user email is not verified by provider")
)

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

func NewGoogleVerifier(clientID, tokenInfoURL string, client *http.Client) *GoogleVerifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if tokenInfoURL == "" {
		tokenInfoURL = "https://oauth2.googleapis.com/tokeninfo"
	}
	return &GoogleVerifier{
		ClientID:     clientID,
		TokenInfoURL: tokenInfoURL,
		HTTPClient:   client,
	}
}

func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	token := strings.TrimSpace(idToken)
	if token == "" {
		return Identity{}, ErrInvalidToken
	}

	endpoint := g.TokenInfoURL
	if endpoint == "" {
		endpoint = "https://oauth2.googleapis.com/tokeninfo"
	}

	reqURL := fmt.Sprintf("%s?id_token=%s", endpoint, url.QueryEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return Identity{}, fmt.Errorf("failed to create auth request: %w", err)
	}

	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return Identity{}, ErrInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		return Identity{}, fmt.Errorf("%w: HTTP status %d", ErrProviderUnavailable, resp.StatusCode)
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Identity{}, fmt.Errorf("%w: invalid response json", ErrInvalidToken)
	}

	if errDesc, ok := raw["error_description"].(string); ok && errDesc != "" {
		return Identity{}, fmt.Errorf("%w: %s", ErrInvalidToken, errDesc)
	}

	sub, _ := raw["sub"].(string)
	if strings.TrimSpace(sub) == "" {
		return Identity{}, ErrInvalidToken
	}

	if g.ClientID != "" {
		aud, _ := raw["aud"].(string)
		if aud != g.ClientID {
			return Identity{}, ErrInvalidAudience
		}
	}
	if iss, ok := raw["iss"].(string); ok && iss != "" {
		if iss != "accounts.google.com" && iss != "https://accounts.google.com" {
			return Identity{}, ErrInvalidIssuer
		}
	}
	email, _ := raw["email"].(string)
	if strings.TrimSpace(email) == "" {
		return Identity{}, ErrUnverifiedEmail
	}
	isVerified := false
	switch v := raw["email_verified"].(type) {
	case bool:
		isVerified = v
	case string:
		isVerified = (strings.ToLower(v) == "true")
	}
	if !isVerified {
		return Identity{}, ErrUnverifiedEmail
	}
	name, _ := raw["name"].(string)
	return Identity{
		Subject: sub,
		Email:   email,
		Name:    name,
	}, nil
}
