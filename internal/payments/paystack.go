package payments

import (
	"context"
	"errors"
	"net/http"
)

var ErrNotImplemented = errors.New("paystack integration not implemented")
var ErrInvalidSignature = errors.New("invalid webhook signature")
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

func (p *PaystackClient) Initialize(context.Context, InitializeRequest) (InitializeResponse, error) {
	return InitializeResponse{}, ErrNotImplemented
}
func (p *PaystackClient) VerifyWebhookSignature([]byte, string) error { return ErrNotImplemented }
func (p *PaystackClient) ParseWebhook([]byte) (WebhookEvent, error) {
	return WebhookEvent{}, ErrNotImplemented
}
