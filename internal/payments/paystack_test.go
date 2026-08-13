package payments_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/payments"
)

const testSecret = "sk_test_not_a_real_key"

type capturedRequest struct {
	Method string
	Path   string
	Auth   string
	CType  string
	Body   map[string]any
}

// paystackServer records the request it receives and replies with a fixed
// status and body.
func paystackServer(t *testing.T, status int, body string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Auth = r.Header.Get("Authorization")
		captured.CType = r.Header.Get("Content-Type")
		_ = json.Unmarshal(raw, &captured.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

func clientFor(srv *httptest.Server) *payments.PaystackClient {
	return &payments.PaystackClient{
		SecretKey:  testSecret,
		BaseURL:    srv.URL,
		HTTPClient: &http.Client{Timeout: 2 * time.Second},
	}
}

const okInitializeBody = `{
  "status": true,
  "message": "Authorization URL created",
  "data": {
    "authorization_url": "https://checkout.paystack.com/abc123",
    "access_code": "abc123",
    "reference": "maoni_user_1_170000"
  }
}`

func TestInitializeSendsAuthenticatedRequest(t *testing.T) {
	srv, got := paystackServer(t, http.StatusOK, okInitializeBody)

	resp, err := clientFor(srv).Initialize(context.Background(), payments.InitializeRequest{
		UserID:    "usr_1",
		Email:     "user@example.com",
		Amount:    500000,
		PlanCode:  "PLN_mock",
		Reference: "maoni_user_1_170000",
	})
	if err != nil {
		t.Fatal(err)
	}

	if got.Method != http.MethodPost || got.Path != "/transaction/initialize" {
		t.Fatalf("called %s %s", got.Method, got.Path)
	}
	if got.Auth != "Bearer "+testSecret {
		t.Fatalf("authorization header = %q", got.Auth)
	}
	if !strings.HasPrefix(got.CType, "application/json") {
		t.Fatalf("content-type = %q", got.CType)
	}
	if got.Body["email"] != "user@example.com" || got.Body["plan"] != "PLN_mock" {
		t.Fatalf("request body = %v", got.Body)
	}
	if amount, _ := got.Body["amount"].(float64); int64(amount) != 500000 {
		t.Fatalf("amount = %v", got.Body["amount"])
	}
	if got.Body["reference"] != "maoni_user_1_170000" {
		t.Fatalf("reference = %v", got.Body["reference"])
	}
	// The Maoni user ID has to travel in metadata: it is what Paystack echoes
	// back on the webhook, and it is how the callback finds the user.
	meta, ok := got.Body["metadata"].(map[string]any)
	if !ok || meta["user_id"] != "usr_1" {
		t.Fatalf("metadata = %v, want user_id usr_1", got.Body["metadata"])
	}

	if resp.AuthorizationURL != "https://checkout.paystack.com/abc123" ||
		resp.AccessCode != "abc123" ||
		resp.Reference != "maoni_user_1_170000" {
		t.Fatalf("unexpected response %+v", resp)
	}
}

func TestInitializeHandlesTrailingSlashInBaseURL(t *testing.T) {
	srv, got := paystackServer(t, http.StatusOK, okInitializeBody)
	c := clientFor(srv)
	c.BaseURL = srv.URL + "/"

	if _, err := c.Initialize(context.Background(), payments.InitializeRequest{Email: "u@example.com", Amount: 100, Reference: "r"}); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/transaction/initialize" {
		t.Fatalf("path = %q", got.Path)
	}
}

func TestInitializeFailureModes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"status":false,"message":"Invalid key"}`},
		{"server error", http.StatusBadGateway, `{}`},
		{"provider declared failure", http.StatusOK, `{"status":false,"message":"Declined"}`},
		{"malformed json", http.StatusOK, `{not json`},
		{"missing authorization url", http.StatusOK, `{"status":true,"data":{"reference":"ref_1"}}`},
		{"missing reference", http.StatusOK, `{"status":true,"data":{"authorization_url":"https://x.test"}}`},
		{"empty body", http.StatusOK, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := paystackServer(t, tc.status, tc.body)
			_, err := clientFor(srv).Initialize(context.Background(), payments.InitializeRequest{
				UserID: "usr_1", Email: "user@example.com", Amount: 500000, Reference: "ref_1",
			})
			if !errors.Is(err, payments.ErrProvider) {
				t.Fatalf("err = %v, want ErrProvider", err)
			}
		})
	}
}

func TestInitializeRequiresConfiguredSecret(t *testing.T) {
	srv, _ := paystackServer(t, http.StatusOK, okInitializeBody)
	c := clientFor(srv)
	c.SecretKey = ""

	if _, err := c.Initialize(context.Background(), payments.InitializeRequest{Email: "u@example.com", Amount: 1, Reference: "r"}); !errors.Is(err, payments.ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
}

func TestInitializeRespectsContext(t *testing.T) {
	// release guarantees the handler returns even if the server never notices
	// the client going away, so shutting the test server down cannot block.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := clientFor(srv).Initialize(ctx, payments.InitializeRequest{Email: "u@example.com", Amount: 1, Reference: "r"})
	if !errors.Is(err, payments.ErrProvider) {
		t.Fatalf("err = %v, want ErrProvider", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("call did not honour the context deadline (took %s)", elapsed)
	}
}

func sign(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	c := &payments.PaystackClient{SecretKey: testSecret}
	body := []byte(`{"event":"charge.success","data":{"id":1}}`)

	if err := c.VerifyWebhookSignature(body, sign(t, testSecret, body)); err != nil {
		t.Fatalf("a correctly signed body was rejected: %v", err)
	}
	// Paystack sends lowercase hex; accepting either case is harmless.
	if err := c.VerifyWebhookSignature(body, strings.ToUpper(sign(t, testSecret, body))); err != nil {
		t.Fatalf("uppercase hex signature was rejected: %v", err)
	}
}

func TestVerifyWebhookSignatureRejections(t *testing.T) {
	body := []byte(`{"event":"charge.success","data":{"id":1}}`)
	tampered := []byte(`{"event":"charge.success","data":{"id":2}}`)

	cases := []struct {
		name      string
		secret    string
		body      []byte
		signature string
	}{
		{"signed with a different key", testSecret, body, sign(t, "another-secret", body)},
		{"body tampered after signing", testSecret, tampered, sign(t, testSecret, body)},
		{"missing header", testSecret, body, ""},
		{"not hex", testSecret, body, "zzzz"},
		{"truncated digest", testSecret, body, sign(t, testSecret, body)[:32]},
		// Fail closed: an unconfigured secret must never accept a webhook.
		{"secret not configured", "", body, sign(t, testSecret, body)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &payments.PaystackClient{SecretKey: tc.secret}
			if err := c.VerifyWebhookSignature(tc.body, tc.signature); !errors.Is(err, payments.ErrInvalidSignature) {
				t.Fatalf("err = %v, want ErrInvalidSignature", err)
			}
		})
	}
}

func TestParseWebhookChargeSuccess(t *testing.T) {
	body := []byte(`{
	  "event": "charge.success",
	  "data": {
	    "id": 302961,
	    "reference": "maoni_usr_1_170000",
	    "status": "success",
	    "customer": {"email": "user@example.com"},
	    "plan": {"plan_code": "PLN_mock"},
	    "metadata": {"user_id": "usr_1"}
	  }
	}`)

	c := &payments.PaystackClient{SecretKey: testSecret}
	event, err := c.ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "charge.success" || event.Status != "active" {
		t.Fatalf("unexpected %+v", event)
	}
	if event.Reference != "maoni_usr_1_170000" || event.UserID != "usr_1" {
		t.Fatalf("correlation fields lost: %+v", event)
	}
	if event.PlanCode != "PLN_mock" || event.Email != "user@example.com" {
		t.Fatalf("unexpected %+v", event)
	}
	// The object ID is scoped per event type, so the type belongs in the key.
	if !strings.HasPrefix(event.ID, "charge.success:") || !strings.HasSuffix(event.ID, "302961") {
		t.Fatalf("event ID = %q", event.ID)
	}
}

// Paystack sometimes echoes metadata back as a JSON-encoded string.
func TestParseWebhookAcceptsStringEncodedMetadata(t *testing.T) {
	body := []byte(`{"event":"charge.success","data":{"id":1,"status":"success","metadata":"{\"user_id\":\"usr_7\"}"}}`)

	event, err := (&payments.PaystackClient{}).ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if event.UserID != "usr_7" {
		t.Fatalf("user id = %q, want usr_7", event.UserID)
	}
}

func TestParseWebhookEventTypeMapping(t *testing.T) {
	cases := []struct {
		event  string
		data   string
		status string
	}{
		{"charge.success", `{"id":1,"status":"success"}`, "active"},
		{"charge.success", `{"id":1,"status":"failed"}`, "failed"},
		{"subscription.create", `{"id":2,"subscription_code":"SUB_1"}`, "active"},
		{"subscription.disable", `{"id":3}`, "cancelled"},
		{"subscription.not_renew", `{"id":4}`, "cancelled"},
		{"invoice.payment_failed", `{"id":5}`, "past_due"},
	}
	for _, tc := range cases {
		t.Run(tc.event+"/"+tc.status, func(t *testing.T) {
			body := []byte(`{"event":"` + tc.event + `","data":` + tc.data + `}`)
			event, err := (&payments.PaystackClient{}).ParseWebhook(body)
			if err != nil {
				t.Fatal(err)
			}
			if event.Status != tc.status {
				t.Fatalf("status = %q, want %q", event.Status, tc.status)
			}
		})
	}
}

func TestParseWebhookRejections(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"malformed json", `{not json`, payments.ErrProvider},
		{"no event type", `{"data":{"id":1}}`, payments.ErrProvider},
		// Without a stable key a retry is indistinguishable from a new event.
		{"nothing to deduplicate on", `{"event":"charge.success","data":{"status":"success"}}`, payments.ErrProvider},
		{"event we do not handle", `{"event":"customer.identification.failed","data":{"id":1}}`, payments.ErrUnsupportedEvent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&payments.PaystackClient{}).ParseWebhook([]byte(tc.body))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A subscription event carries no transaction reference; it must still produce
// a usable idempotency key.
func TestParseWebhookFallsBackToSubscriptionCodeForEventID(t *testing.T) {
	body := []byte(`{"event":"subscription.disable","data":{"subscription_code":"SUB_xyz","plan":{"plan_code":"PLN_mock"}}}`)

	event, err := (&payments.PaystackClient{}).ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != "subscription.disable:SUB_xyz" {
		t.Fatalf("event ID = %q", event.ID)
	}
	if event.PlanCode != "PLN_mock" {
		t.Fatalf("plan code = %q", event.PlanCode)
	}
}
