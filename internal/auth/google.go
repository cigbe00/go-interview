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

var (
	ErrInvalidToken        = errors.New("invalid identity token")
	ErrProviderUnavailable = errors.New("identity provider unavailable")
	// ErrNotConfigured is a deployment fault, not a bad credential, so it
	// wraps ErrProviderUnavailable and surfaces as 502 rather than 401.
	ErrNotConfigured = fmt.Errorf("%w: google client id is not configured", ErrProviderUnavailable)
)

const (
	defaultVerifyTimeout = 5 * time.Second
	maxTokenInfoBody     = 1 << 20 // 1 MiB
	// clockSkew tolerates small clock differences between us and Google when
	// checking expiry.
	clockSkew = 30 * time.Second
)

// validIssuers are the two issuer spellings Google uses for ID tokens.
var validIssuers = map[string]struct{}{
	"accounts.google.com":         {},
	"https://accounts.google.com": {},
}

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

// tokenInfo is Google's tokeninfo response. The endpoint returns exp and
// email_verified as JSON strings while the decoded JWT payload uses native
// types, so both spellings are accepted.
type tokenInfo struct {
	Issuer        string    `json:"iss"`
	Audience      string    `json:"aud"`
	Subject       string    `json:"sub"`
	Email         string    `json:"email"`
	EmailVerified flexBool  `json:"email_verified"`
	Expiry        flexInt64 `json:"exp"`
	Name          string    `json:"name"`
	Error         string    `json:"error"`
	ErrorDesc     string    `json:"error_description"`
}

// Verify exchanges a Google ID token for a normalized Identity.
//
// Errors are split deliberately: a token the provider or our own checks reject
// wraps ErrInvalidToken (the caller's credential is bad), while a transport,
// status or decoding failure wraps ErrProviderUnavailable (our dependency is
// bad). The API layer maps those to 401 and 502 respectively.
func (g *GoogleVerifier) Verify(ctx context.Context, idToken string) (Identity, error) {
	idToken = strings.TrimSpace(idToken)
	if idToken == "" {
		return Identity{}, fmt.Errorf("%w: empty token", ErrInvalidToken)
	}
	if strings.TrimSpace(g.ClientID) == "" {
		return Identity{}, ErrNotConfigured
	}

	info, err := g.fetchTokenInfo(ctx, idToken)
	if err != nil {
		return Identity{}, err
	}
	if err := g.validate(info); err != nil {
		return Identity{}, err
	}
	return Identity{
		Subject: info.Subject,
		Email:   strings.ToLower(strings.TrimSpace(info.Email)),
		Name:    info.Name,
	}, nil
}

func (g *GoogleVerifier) fetchTokenInfo(ctx context.Context, idToken string) (tokenInfo, error) {
	endpoint, err := g.endpoint(idToken)
	if err != nil {
		return tokenInfo{}, fmt.Errorf("%w: bad tokeninfo url: %v", ErrProviderUnavailable, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return tokenInfo{}, fmt.Errorf("%w: build request: %v", ErrProviderUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient().Do(req)
	if err != nil {
		return tokenInfo{}, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxTokenInfoBody))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenInfoBody))
	if err != nil {
		return tokenInfo{}, fmt.Errorf("%w: read response: %v", ErrProviderUnavailable, err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnauthorized:
		// Google reports a malformed, expired or unknown token as 400/401.
		return tokenInfo{}, fmt.Errorf("%w: rejected by google", ErrInvalidToken)
	default:
		return tokenInfo{}, fmt.Errorf("%w: tokeninfo returned status %d", ErrProviderUnavailable, resp.StatusCode)
	}

	var info tokenInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return tokenInfo{}, fmt.Errorf("%w: malformed tokeninfo response: %v", ErrProviderUnavailable, err)
	}
	// A 200 carrying an error field is still a rejected token.
	if info.Error != "" {
		return tokenInfo{}, fmt.Errorf("%w: rejected by google", ErrInvalidToken)
	}
	return info, nil
}

func (g *GoogleVerifier) endpoint(idToken string) (string, error) {
	u, err := url.Parse(g.TokenInfoURL)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("tokeninfo url %q is not absolute", g.TokenInfoURL)
	}
	q := u.Query()
	q.Set("id_token", idToken)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (g *GoogleVerifier) httpClient() *http.Client {
	if g.HTTPClient != nil {
		return g.HTTPClient
	}
	return &http.Client{Timeout: defaultVerifyTimeout}
}

func (g *GoogleVerifier) validate(info tokenInfo) error {
	if _, ok := validIssuers[info.Issuer]; !ok {
		return fmt.Errorf("%w: unexpected issuer", ErrInvalidToken)
	}
	// The audience check is what stops an ID token minted for a different
	// application from being replayed against this one.
	if info.Audience == "" || info.Audience != g.ClientID {
		return fmt.Errorf("%w: audience mismatch", ErrInvalidToken)
	}
	if info.Expiry <= 0 {
		return fmt.Errorf("%w: missing expiry", ErrInvalidToken)
	}
	if time.Unix(int64(info.Expiry), 0).Before(time.Now().Add(-clockSkew)) {
		return fmt.Errorf("%w: token expired", ErrInvalidToken)
	}
	// `sub` is the only identifier Google guarantees is stable for an account;
	// email can change hands and must never be used as the account key.
	if strings.TrimSpace(info.Subject) == "" {
		return fmt.Errorf("%w: missing subject", ErrInvalidToken)
	}
	if strings.TrimSpace(info.Email) == "" {
		return fmt.Errorf("%w: missing email", ErrInvalidToken)
	}
	if !bool(info.EmailVerified) {
		return fmt.Errorf("%w: email is not verified", ErrInvalidToken)
	}
	return nil
}

// flexBool accepts either true or "true".
type flexBool bool

func (f *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		*f = false
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return fmt.Errorf("parse bool %q: %w", s, err)
	}
	*f = flexBool(v)
	return nil
}

// flexInt64 accepts either 1700000000 or "1700000000".
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("parse int %q: %w", s, err)
	}
	*f = flexInt64(v)
	return nil
}
