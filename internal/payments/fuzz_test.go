package payments_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/maoni/backend-takehome/internal/payments"
)

// Webhook payloads are attacker-reachable: the endpoint is public, and
// ParseWebhook runs on bytes that have not been authenticated yet in the
// unsigned case. It must never panic, and it must never invent an event.
func FuzzParseWebhook(f *testing.F) {
	seeds := []string{
		`{"event":"charge.success","data":{"id":1,"status":"success","reference":"r","metadata":{"user_id":"u"}}}`,
		`{"event":"subscription.disable","data":{"subscription_code":"SUB_1","plan":{"plan_code":"PLN"}}}`,
		`{"event":"charge.success","data":{"id":"1","metadata":"{\"user_id\":\"u\"}"}}`,
		`{"event":"charge.success","data":{"plan":"PLN_string_form","id":2}}`,
		`{"event":"","data":{}}`,
		`{"data":null}`,
		`{}`,
		``,
		`null`,
		`[]`,
		`{"event":"charge.success","data":[1,2,3]}`,
		`{"event":"charge.success","data":{"id":1e309}}`,
		`{"event":"charge.success","data":{"metadata":12345}}`,
		strings.Repeat(`{"event":"charge.success",`, 50),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	client := &payments.PaystackClient{SecretKey: "sk_test_fuzz"}
	f.Fuzz(func(t *testing.T, body []byte) {
		event, err := client.ParseWebhook(body)
		if err != nil {
			// Every rejection must be a classified error the caller can act on,
			// not a bare or wrapped-nothing error.
			if !errors.Is(err, payments.ErrProvider) && !errors.Is(err, payments.ErrUnsupportedEvent) {
				t.Fatalf("unclassified parse error %v for %q", err, body)
			}
			return
		}
		// A successfully parsed event must be usable: without an ID there is no
		// idempotency key, and without a status there is nothing to apply.
		if event.ID == "" {
			t.Fatalf("accepted an event with no idempotency key: %q", body)
		}
		if event.Status == "" {
			t.Fatalf("accepted an event with no status: %q", body)
		}
		if event.Type == "" {
			t.Fatalf("accepted an event with no type: %q", body)
		}
	})
}

// Signature verification runs on every inbound webhook, before anything else
// has vetted the input. It must never panic, and must never accept a body that
// was not signed with our secret.
func FuzzVerifyWebhookSignature(f *testing.F) {
	f.Add([]byte(`{"event":"charge.success"}`), "")
	f.Add([]byte(`{"event":"charge.success"}`), "deadbeef")
	f.Add([]byte(``), "00")
	f.Add([]byte(`{}`), strings.Repeat("a", 128))
	f.Add([]byte(`{}`), strings.Repeat("f", 1000))
	f.Add([]byte(`{}`), "not-hex-at-all")
	f.Add([]byte(`{}`), " 0011 ")

	client := &payments.PaystackClient{SecretKey: "sk_test_fuzz"}
	f.Fuzz(func(t *testing.T, body []byte, signature string) {
		err := client.VerifyWebhookSignature(body, signature)
		if err == nil {
			// The only way to get here is to have produced a valid HMAC-SHA512
			// of this exact body under the secret. Confirm it really is one
			// rather than a comparison that let something through.
			if want := sign(t, "sk_test_fuzz", body); !strings.EqualFold(want, strings.TrimSpace(signature)) {
				t.Fatalf("accepted signature %q for body %q; correct digest is %q", signature, body, want)
			}
			return
		}
		if !errors.Is(err, payments.ErrInvalidSignature) {
			t.Fatalf("unclassified signature error %v", err)
		}
	})
}
