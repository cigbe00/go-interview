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
	"net/http"
	"strings"
	"time"
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

func NewPaystackClient(secretKey, baseURL string, client *http.Client) *PaystackClient {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if baseURL == "" {
		baseURL = "https://api.paystack.co"
	}
	return &PaystackClient{
		SecretKey:  secretKey,
		BaseURL:    baseURL,
		HTTPClient: client,
	}
}

type paystackAPIResponse struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		AuthorizationURL string `json:"authorization_url"`
		AccessCode       string `json:"access_code"`
		Reference        string `json:"reference"`
	} `json:"data"`
}

func (p *PaystackClient) Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error) {
	if req.Email == "" || req.Amount <= 0 || req.Reference == "" {
		return InitializeResponse{}, fmt.Errorf("%w: invalid initialization request parameters", ErrProvider)
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: failed to marshal request: %v", ErrProvider, err)
	}

	baseURL := p.BaseURL
	if baseURL == "" {
		baseURL = "https://api.paystack.co"
	}

	endpoint := fmt.Sprintf("%s/transaction/initialize", strings.TrimRight(baseURL, "/"))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: failed to construct request: %v", ErrProvider, err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+p.SecretKey)
	httpReq.Header.Set("Content-Type", "application/json")

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: network error: %v", ErrProvider, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return InitializeResponse{}, fmt.Errorf("%w: paystack returned status %d", ErrProvider, resp.StatusCode)
	}

	var res paystackAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || !res.Status {
		return InitializeResponse{}, fmt.Errorf("%w: failed to decode paystack response", ErrProvider)
	}

	return InitializeResponse{
		AuthorizationURL: res.Data.AuthorizationURL,
		AccessCode:       res.Data.AccessCode,
		Reference:        res.Data.Reference,
	}, nil
}

func (p *PaystackClient) VerifyWebhookSignature(body []byte, signature string) error {
	sig := strings.TrimSpace(signature)
	if sig == "" || p.SecretKey == "" {
		return ErrInvalidSignature
	}

	mac := hmac.New(sha512.New, []byte(p.SecretKey))
	mac.Write(body)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(strings.ToLower(sig)), []byte(strings.ToLower(expectedSig))) {
		return ErrInvalidSignature
	}
	return nil
}

type paystackWebhookBody struct {
	Event string `json:"event"`
	Data  struct {
		ID        int64  `json:"id"`
		Reference string `json:"reference"`
		Status    string `json:"status"`
		Customer  struct {
			Email string `json:"email"`
		} `json:"customer"`
		Plan struct {
			PlanCode string `json:"plan_code"`
		} `json:"plan"`
	} `json:"data"`
}

func (p *PaystackClient) ParseWebhook(body []byte) (WebhookEvent, error) {
	var payload paystackWebhookBody
	if err := json.Unmarshal(body, &payload); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed json", ErrProvider)
	}

	if payload.Data.Reference == "" {
		return WebhookEvent{}, fmt.Errorf("%w: missing transaction reference", ErrProvider)
	}

	eventID := fmt.Sprintf("evt_%d", payload.Data.ID)
	if payload.Data.ID == 0 {
		eventID = fmt.Sprintf("evt_%s", payload.Data.Reference)
	}

	status := payload.Data.Status
	if payload.Event == "charge.success" && status == "" {
		status = "success"
	}

	return WebhookEvent{
		ID:        eventID,
		Type:      payload.Event,
		Reference: payload.Data.Reference,
		Email:     payload.Data.Customer.Email,
		PlanCode:  payload.Data.Plan.PlanCode,
		Status:    status,
	}, nil
}
