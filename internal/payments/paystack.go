package payments

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

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

func (p *PaystackClient) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	if strings.TrimSpace(p.SecretKey) == "" || strings.TrimSpace(p.BaseURL) == "" {
		return InitializeResponse{}, ErrProvider
	}
	body := map[string]any{
		"email":     req.Email,
		"amount":    req.Amount,
		"reference": req.Reference,
		"metadata": map[string]string{
			"user_id":   req.UserID,
			"plan_code": strings.TrimSpace(req.PlanCode),
		},
	}
	if strings.TrimSpace(req.PlanCode) != "" {
		body["plan"] = req.PlanCode
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return InitializeResponse{}, ErrProvider
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(p.BaseURL, "/transaction/initialize"), bytes.NewReader(payload))
	if err != nil {
		return InitializeResponse{}, ErrProvider
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.SecretKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := p.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return InitializeResponse{}, ErrProvider
	}
	defer resp.Body.Close()

	var apiResp struct {
		Status  bool   `json:"status"`
		Message string `json:"message,omitempty"`
		Data    struct {
			AuthorizationURL string `json:"authorization_url"`
			AccessCode       string `json:"access_code"`
			Reference        string `json:"reference"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&apiResp); err != nil {
		return InitializeResponse{}, ErrProvider
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !apiResp.Status {
		return InitializeResponse{}, fmt.Errorf("%w: %s", ErrProvider, apiResp.Message)
	}
	if apiResp.Data.AuthorizationURL == "" || apiResp.Data.Reference == "" {
		return InitializeResponse{}, ErrProvider
	}
	return InitializeResponse{
		AuthorizationURL: apiResp.Data.AuthorizationURL,
		AccessCode:       apiResp.Data.AccessCode,
		Reference:        apiResp.Data.Reference,
	}, nil
}

func (p *PaystackClient) VerifyWebhookSignature(body []byte, signature string) error {
	if signature == "" || len(body) == 0 {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha512.New, []byte(p.SecretKey))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return ErrInvalidSignature
	}
	return nil
}

type webhookPayload struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type webhookData struct {
	ID               json.Number `json:"id"`
	Reference        string      `json:"reference"`
	SubscriptionCode string      `json:"subscription_code"`
	Plan             struct {
		PlanCode string `json:"plan_code"`
	} `json:"plan"`
	Metadata struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
	Customer struct {
		Email string `json:"email"`
	} `json:"customer"`
}

func (p *PaystackClient) ParseWebhook(body []byte) (WebhookEvent, error) {
	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return WebhookEvent{}, fmt.Errorf("decode webhook payload: %w", err)
	}
	status, ok := supportedStatus(payload.Event)
	if !ok {
		return WebhookEvent{}, fmt.Errorf("unsupported event type: %s", payload.Event)
	}
	var data webhookData
	if err := json.Unmarshal(payload.Data, &data); err != nil {
		return WebhookEvent{}, fmt.Errorf("decode webhook data: %w", err)
	}
	id := data.Reference
	if id == "" {
		id = data.SubscriptionCode
	}
	if id == "" && data.ID != "" {
		id = data.ID.String()
	}
	if id == "" {
		return WebhookEvent{}, fmt.Errorf("webhook data missing event identity")
	}
	return WebhookEvent{
		ID:        fmt.Sprintf("%s:%s", payload.Event, id),
		Type:      payload.Event,
		Reference: data.Reference,
		UserID:    data.Metadata.UserID,
		Email:     data.Customer.Email,
		PlanCode:  data.Plan.PlanCode,
		Status:    status,
	}, nil
}

func supportedStatus(event string) (string, bool) {
	switch event {
	case "charge.success", "subscription.create", "invoice.create":
		return "active", true
	default:
		return "", false
	}
}

func endpoint(base, path string) string {
	return strings.TrimRight(base, "/") + path
}
