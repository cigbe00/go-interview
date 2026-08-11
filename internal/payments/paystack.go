package payments

import (
	"context"
	"errors"
)

var ErrNotImplemented = errors.New("paystack integration not implemented")
var ErrInvalidSignature = errors.New("invalid webhook signature")

type InitializeRequest struct {
	Email     string `json:"email"`
	Amount    int64  `json:"amount"`
	PlanCode  string `json:"plan"`
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
	Email     string
	PlanCode  string
	Status    string
}

type Provider interface {
	Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error)
	VerifyWebhookSignature(body []byte, signature string) error
	ParseWebhook(body []byte) (WebhookEvent, error)
}

type PaystackClient struct {
	SecretKey string
	BaseURL   string
}

func (p *PaystackClient) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	return InitializeResponse{}, ErrNotImplemented
}

func (p *PaystackClient) VerifyWebhookSignature(body []byte, signature string) error {
	return ErrNotImplemented
}

func (p *PaystackClient) ParseWebhook(body []byte) (WebhookEvent, error) {
	return WebhookEvent{}, ErrNotImplemented
}
