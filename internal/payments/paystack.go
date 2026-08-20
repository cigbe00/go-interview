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
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidSignature = errors.New("invalid webhook signature")
	ErrProvider         = errors.New("payment provider error")
	ErrInvalidWebhook   = errors.New("invalid payment webhook")
)

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

func (p *PaystackClient) Initialize(ctx context.Context, request InitializeRequest) (InitializeResponse, error) {
	if strings.TrimSpace(p.SecretKey) == "" || strings.TrimSpace(p.BaseURL) == "" {
		return InitializeResponse{}, fmt.Errorf("%w: paystack client is not configured", ErrProvider)
	}
	payload := struct {
		Email     string `json:"email"`
		Amount    int64  `json:"amount"`
		PlanCode  string `json:"plan,omitempty"`
		Reference string `json:"reference"`
		Metadata  struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}{Email: request.Email, Amount: request.Amount, PlanCode: request.PlanCode, Reference: request.Reference}
	payload.Metadata.UserID = request.UserID
	body, err := json.Marshal(payload)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: encode initialize request", ErrProvider)
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + "/transaction/initialize"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: create initialize request", ErrProvider)
	}
	req.Header.Set("Authorization", "Bearer "+p.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: initialize request failed: %v", ErrProvider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return InitializeResponse{}, fmt.Errorf("%w: initialize returned %s", ErrProvider, resp.Status)
	}
	var providerResponse struct {
		Status  bool               `json:"status"`
		Message string             `json:"message"`
		Data    InitializeResponse `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&providerResponse); err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: decode initialize response", ErrProvider)
	}
	data := providerResponse.Data
	if !providerResponse.Status || strings.TrimSpace(data.AuthorizationURL) == "" || strings.TrimSpace(data.AccessCode) == "" || data.Reference != request.Reference {
		return InitializeResponse{}, fmt.Errorf("%w: initialize response was unsuccessful or incomplete", ErrProvider)
	}
	return data, nil
}

func (p *PaystackClient) VerifyWebhookSignature(body []byte, signature string) error {
	signatureBytes, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil || len(signatureBytes) == 0 || strings.TrimSpace(p.SecretKey) == "" {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha512.New, []byte(p.SecretKey))
	_, _ = mac.Write(body)
	if !hmac.Equal(signatureBytes, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

func (p *PaystackClient) ParseWebhook(body []byte) (WebhookEvent, error) {
	var payload struct {
		Event string `json:"event"`
		Data  struct {
			ID        json.RawMessage `json:"id"`
			Reference string          `json:"reference"`
			Status    string          `json:"status"`
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
	if err := json.Unmarshal(body, &payload); err != nil || strings.TrimSpace(payload.Event) == "" {
		return WebhookEvent{}, fmt.Errorf("%w: malformed payload", ErrInvalidWebhook)
	}
	event := WebhookEvent{Type: payload.Event}
	if payload.Event != "charge.success" {
		return event, nil
	}
	id, err := webhookDataID(payload.Data.ID)
	if err != nil || strings.TrimSpace(payload.Data.Reference) == "" || strings.TrimSpace(payload.Data.Metadata.UserID) == "" {
		return WebhookEvent{}, fmt.Errorf("%w: required charge data is missing", ErrInvalidWebhook)
	}
	event.ID = payload.Event + ":" + id
	event.Reference = strings.TrimSpace(payload.Data.Reference)
	event.UserID = strings.TrimSpace(payload.Data.Metadata.UserID)
	event.Email = strings.ToLower(strings.TrimSpace(payload.Data.Customer.Email))
	event.PlanCode = strings.TrimSpace(payload.Data.Plan.PlanCode)
	event.Status = "active"
	return event, nil
}

func webhookDataID(raw json.RawMessage) (string, error) {
	value := strings.Trim(string(raw), `"`)
	if value == "" {
		return "", errors.New("missing transaction id")
	}
	if _, err := strconv.ParseInt(value, 10, 64); err != nil {
		return "", err
	}
	return value, nil
}
