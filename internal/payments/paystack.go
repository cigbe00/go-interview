package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrInvalidSignature = errors.New("invalid webhook signature")
var ErrInvalidWebhook = errors.New("invalid webhook payload")
var ErrProvider = errors.New("payment provider error")

type InitializeRequest struct {
	UserID    string `json:"-"`
	Email     string `json:"email"`
	Amount    int64  `json:"amount"`
	PlanCode  string `json:"plan,omitempty"`
	Reference string `json:"reference"`
}
type InitializeResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}
type WebhookEvent struct {
	ID        string
	Type      string
	Reference string
	UserID    string
	Email     string
	PlanCode  string
	Status    string
}
type Provider interface {
	Initialize(context.Context, InitializeRequest) (InitializeResponse, error)
	VerifyWebhookSignature([]byte, string) error
	ParseWebhook([]byte) (WebhookEvent, error)
}

type PaystackClient struct {
	SecretKey  string
	BaseURL    string
	HTTPClient *http.Client
}

func (p *PaystackClient) Initialize(ctx context.Context, in InitializeRequest) (InitializeResponse, error) {
	if strings.TrimSpace(p.SecretKey) == "" || strings.TrimSpace(p.BaseURL) == "" {
		return InitializeResponse{}, fmt.Errorf("%w: paystack is not configured", ErrProvider)
	}
	payload := struct {
		Email     string         `json:"email"`
		Amount    int64          `json:"amount"`
		PlanCode  string         `json:"plan,omitempty"`
		Reference string         `json:"reference"`
		Metadata  map[string]any `json:"metadata"`
	}{in.Email, in.Amount, in.PlanCode, in.Reference, map[string]any{"user_id": in.UserID}}
	body, err := json.Marshal(payload)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: encode request", ErrProvider)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/transaction/initialize", bytes.NewReader(body))
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: create request", ErrProvider)
	}
	req.Header.Set("Authorization", "Bearer "+p.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20)
	var envelope struct {
		Status  bool               `json:"status"`
		Message string             `json:"message"`
		Data    InitializeResponse `json:"data"`
	}
	if err := json.NewDecoder(limited).Decode(&envelope); err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: malformed response", ErrProvider)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.Status {
		return InitializeResponse{}, fmt.Errorf("%w: %s", ErrProvider, strings.TrimSpace(envelope.Message))
	}
	if envelope.Data.AuthorizationURL == "" || envelope.Data.AccessCode == "" || envelope.Data.Reference == "" {
		return InitializeResponse{}, fmt.Errorf("%w: incomplete response", ErrProvider)
	}
	if envelope.Data.Reference != in.Reference {
		return InitializeResponse{}, fmt.Errorf("%w: reference mismatch", ErrProvider)
	}
	return envelope.Data, nil
}
func (p *PaystackClient) VerifyWebhookSignature(body []byte, signature string) error {
	decoded, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(decoded) == 0 || p.SecretKey == "" {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha512.New, []byte(p.SecretKey))
	_, _ = mac.Write(body)
	if !hmac.Equal(decoded, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}
func (p *PaystackClient) ParseWebhook(body []byte) (WebhookEvent, error) {
	var payload struct {
		Event string `json:"event"`
		Data  struct {
			ID        json.Number `json:"id"`
			Reference string      `json:"reference"`
			Status    string      `json:"status"`
			Customer  struct {
				Email string `json:"email"`
			} `json:"customer"`
			Plan struct {
				PlanCode string `json:"plan_code"`
			} `json:"plan"`
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		} `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed JSON", ErrInvalidWebhook)
	}
	if payload.Event != "charge.success" {
		return WebhookEvent{}, fmt.Errorf("%w: unsupported event %q", ErrInvalidWebhook, payload.Event)
	}
	event := WebhookEvent{ID: payload.Event + ":" + payload.Data.ID.String(), Type: payload.Event, Reference: strings.TrimSpace(payload.Data.Reference), UserID: strings.TrimSpace(payload.Data.Metadata.UserID), Email: strings.TrimSpace(payload.Data.Customer.Email), PlanCode: strings.TrimSpace(payload.Data.Plan.PlanCode), Status: "active"}
	if payload.Data.ID.String() == "" || event.Reference == "" || event.UserID == "" || event.PlanCode == "" || payload.Data.Status != "success" {
		return WebhookEvent{}, ErrInvalidWebhook
	}
	return event, nil
}
