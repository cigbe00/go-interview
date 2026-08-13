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
	"net/url"
	"strings"
	"time"
)

var (
	ErrInvalidSignature = errors.New("invalid webhook signature")
	ErrProvider         = errors.New("payment provider error")
	// ErrUnsupportedEvent marks a well-formed webhook we deliberately ignore.
	ErrUnsupportedEvent = errors.New("unsupported webhook event")
)

const (
	defaultProviderTimeout = 10 * time.Second
	maxProviderBody        = 1 << 20 // 1 MiB
	initializePath         = "/transaction/initialize"
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

// initializeWire is the on-the-wire request. It exists so the internal
// InitializeRequest stays a plain DTO while the Maoni user ID travels in
// Paystack's metadata field, which is echoed back on webhooks and is what
// lets a callback be tied to the user who started the payment.
type initializeWire struct {
	Email     string   `json:"email"`
	Amount    int64    `json:"amount"`
	Plan      string   `json:"plan,omitempty"`
	Reference string   `json:"reference"`
	Metadata  metadata `json:"metadata"`
}

type metadata struct {
	UserID string `json:"user_id"`
}

type initializeEnvelope struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    struct {
		AuthorizationURL string `json:"authorization_url"`
		AccessCode       string `json:"access_code"`
		Reference        string `json:"reference"`
	} `json:"data"`
}

func (p *PaystackClient) Initialize(ctx context.Context, in InitializeRequest) (InitializeResponse, error) {
	if strings.TrimSpace(p.SecretKey) == "" {
		return InitializeResponse{}, fmt.Errorf("%w: secret key is not configured", ErrProvider)
	}
	endpoint, err := url.JoinPath(p.BaseURL, initializePath)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: bad base url: %v", ErrProvider, err)
	}

	payload, err := json.Marshal(initializeWire{
		Email:     in.Email,
		Amount:    in.Amount,
		Plan:      in.PlanCode,
		Reference: in.Reference,
		Metadata:  metadata{UserID: in.UserID},
	})
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: encode request: %v", ErrProvider, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: build request: %v", ErrProvider, err)
	}
	req.Header.Set("Authorization", "Bearer "+p.SecretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: %v", ErrProvider, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProviderBody))
		_ = resp.Body.Close()
	}()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBody))
	if err != nil {
		return InitializeResponse{}, fmt.Errorf("%w: read response: %v", ErrProvider, err)
	}

	var env initializeEnvelope
	decodeErr := json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Prefer Paystack's own message when it decoded; never echo the raw
		// body back, since it can carry provider detail we should not relay.
		if decodeErr == nil && env.Message != "" {
			return InitializeResponse{}, fmt.Errorf("%w: status %d: %s", ErrProvider, resp.StatusCode, env.Message)
		}
		return InitializeResponse{}, fmt.Errorf("%w: status %d", ErrProvider, resp.StatusCode)
	}
	if decodeErr != nil {
		return InitializeResponse{}, fmt.Errorf("%w: malformed response: %v", ErrProvider, decodeErr)
	}
	// A 200 with status:false is a provider-declared failure.
	if !env.Status {
		message := env.Message
		if strings.TrimSpace(message) == "" {
			message = "initialization declined"
		}
		return InitializeResponse{}, fmt.Errorf("%w: %s", ErrProvider, message)
	}
	if env.Data.AuthorizationURL == "" || env.Data.Reference == "" {
		return InitializeResponse{}, fmt.Errorf("%w: response missing authorization_url or reference", ErrProvider)
	}

	return InitializeResponse{
		AuthorizationURL: env.Data.AuthorizationURL,
		AccessCode:       env.Data.AccessCode,
		Reference:        env.Data.Reference,
	}, nil
}

// VerifyWebhookSignature checks the HMAC-SHA512 of the raw request body
// against the x-paystack-signature header. The body must be the exact bytes
// received: re-encoding it changes the digest.
func (p *PaystackClient) VerifyWebhookSignature(body []byte, signature string) error {
	if strings.TrimSpace(p.SecretKey) == "" {
		// Fail closed. An unconfigured secret must never accept a webhook.
		return fmt.Errorf("%w: webhook secret is not configured", ErrInvalidSignature)
	}
	signature = strings.TrimSpace(signature)
	if signature == "" {
		return fmt.Errorf("%w: missing signature header", ErrInvalidSignature)
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w: signature is not valid hex", ErrInvalidSignature)
	}
	mac := hmac.New(sha512.New, []byte(p.SecretKey))
	mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

type webhookEnvelope struct {
	Event string `json:"event"`
	Data  struct {
		ID        json.Number `json:"id"`
		Reference string      `json:"reference"`
		Status    string      `json:"status"`
		Customer  struct {
			Email string `json:"email"`
		} `json:"customer"`
		Plan             flexPlan        `json:"plan"`
		PlanCode         string          `json:"plan_code"`
		SubscriptionCode string          `json:"subscription_code"`
		Metadata         json.RawMessage `json:"metadata"`
	} `json:"data"`
}

// statusForEvent maps the provider's event vocabulary onto Maoni subscription
// states. Only the events needed for a coherent subscription lifecycle are
// handled; anything else is reported as ErrUnsupportedEvent so the API can
// acknowledge it without applying a change.
var statusForEvent = map[string]string{
	"charge.success":         "active",
	"subscription.create":    "active",
	"invoice.payment_failed": "past_due",
	"subscription.disable":   "cancelled",
	"subscription.not_renew": "cancelled",
}

func (p *PaystackClient) ParseWebhook(body []byte) (WebhookEvent, error) {
	var env webhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: malformed webhook payload: %v", ErrProvider, err)
	}
	if env.Event == "" {
		return WebhookEvent{}, fmt.Errorf("%w: webhook payload has no event type", ErrProvider)
	}

	status, ok := statusForEvent[env.Event]
	if !ok {
		return WebhookEvent{}, fmt.Errorf("%w: %s", ErrUnsupportedEvent, env.Event)
	}
	// charge.success is only an activation when the charge actually succeeded.
	if env.Event == "charge.success" && !strings.EqualFold(env.Data.Status, "success") {
		status = "failed"
	}

	eventID, err := eventID(env)
	if err != nil {
		return WebhookEvent{}, err
	}

	planCode := env.Data.Plan.Code
	if planCode == "" {
		planCode = env.Data.PlanCode
	}

	return WebhookEvent{
		ID:        eventID,
		Type:      env.Event,
		Reference: env.Data.Reference,
		UserID:    parseMetadataUserID(env.Data.Metadata),
		Email:     env.Data.Customer.Email,
		PlanCode:  planCode,
		Status:    status,
	}, nil
}

// eventID derives a stable idempotency key. The provider's object ID is scoped
// per event type, so the type has to be part of the key: the same transaction
// ID legitimately appears on both charge.success and invoice.payment_failed.
func eventID(env webhookEnvelope) (string, error) {
	if id := env.Data.ID.String(); id != "" {
		return env.Event + ":" + id, nil
	}
	if env.Data.SubscriptionCode != "" {
		return env.Event + ":" + env.Data.SubscriptionCode, nil
	}
	if env.Data.Reference != "" {
		return env.Event + ":" + env.Data.Reference, nil
	}
	// Without a stable key a retry cannot be told apart from a new event, so
	// the delivery is rejected rather than risking a double apply.
	return "", fmt.Errorf("%w: webhook payload has no identifier to deduplicate on", ErrProvider)
}

// parseMetadataUserID reads metadata.user_id. Paystack echoes metadata back
// either as an object or as a JSON-encoded string, so both are handled.
func parseMetadataUserID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m metadata
	if err := json.Unmarshal(raw, &m); err == nil {
		return m.UserID
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return ""
	}
	if err := json.Unmarshal([]byte(asString), &m); err != nil {
		return ""
	}
	return m.UserID
}

// flexPlan accepts plan as either an object or the bare plan code string.
type flexPlan struct {
	Code string
}

func (f *flexPlan) UnmarshalJSON(data []byte) error {
	var asObject struct {
		PlanCode string `json:"plan_code"`
	}
	if err := json.Unmarshal(data, &asObject); err == nil {
		f.Code = asObject.PlanCode
		return nil
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		f.Code = asString
		return nil
	}
	// An unusable plan field is not worth failing the whole delivery over.
	return nil
}

func (p *PaystackClient) httpClient() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: defaultProviderTimeout}
}
