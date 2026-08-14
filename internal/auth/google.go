package auth

import (
	"context"
	"encoding/json"
	"errors"
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

type tokenInfo struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Aud     string `json:"aud"`
	Iss     string `json:"iss"`
	Exp     string `json:"exp"`
}

func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	if strings.TrimSpace(idToken) == "" || g.ClientID == "" || g.TokenInfoURL == "" {
		return Identity{}, ErrInvalidToken
	}
	info, err := g.fetchTokenInfo(ctx, idToken)
	if err != nil {
		return Identity{}, err
	}
	if info.Subject == "" || info.Email == "" ||
		info.Aud != g.ClientID ||
		!isGoogleIssuer(info.Iss) ||
		expired(info.Exp) {
		return Identity{}, ErrInvalidToken
	}
	return Identity{Subject: info.Subject, Email: info.Email, Name: info.Name}, nil
}

func (g *GoogleVerifier) fetchTokenInfo(ctx context.Context, idToken string) (tokenInfo, error) {
	u, err := url.Parse(g.TokenInfoURL)
	if err != nil {
		return tokenInfo{}, ErrProviderUnavailable
	}
	q := u.Query()
	q.Set("id_token", idToken)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return tokenInfo{}, ErrProviderUnavailable
	}
	client := g.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return tokenInfo{}, ErrProviderUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		return tokenInfo{}, ErrInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		return tokenInfo{}, ErrProviderUnavailable
	}
	var info tokenInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return tokenInfo{}, ErrInvalidToken
	}
	return info, nil
}

func isGoogleIssuer(iss string) bool {
	return iss == "accounts.google.com" || iss == "https://accounts.google.com"
}

func expired(exp string) bool {
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return true
	}
	return time.Now().Unix() >= unix
}
