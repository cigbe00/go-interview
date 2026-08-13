package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maoni/backend-takehome/internal/config"
)

// keys is every variable Load reads. Tests clear all of them so a value in the
// developer's own environment cannot change the result.
var keys = []string{
	"PORT", "REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB", "REDIS_BUSINESS_TTL_SECONDS",
	"GOOGLE_CLIENT_ID", "GOOGLE_TOKENINFO_URL", "GOOGLE_HTTP_TIMEOUT_SECONDS",
	"PAYSTACK_SECRET_KEY", "PAYSTACK_BASE_URL", "PAYSTACK_HTTP_TIMEOUT_SECONDS",
}

// isolate runs the test in an empty directory with no configuration set, so
// Load sees neither a .env file nor inherited environment variables.
func isolate(t *testing.T) string {
	t.Helper()
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			t.Cleanup(func() { _ = os.Setenv(k, v) })
			_ = os.Unsetenv(k)
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
	}

	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return dir
}

func TestLoadDefaults(t *testing.T) {
	isolate(t)
	cfg := config.Load()

	if cfg.Port != "8080" {
		t.Fatalf("Port = %q", cfg.Port)
	}
	if cfg.RedisAddr != "localhost:6379" || cfg.RedisDB != 0 {
		t.Fatalf("redis defaults = %q db=%d", cfg.RedisAddr, cfg.RedisDB)
	}
	if cfg.RedisBusinessTTL != 5*time.Minute {
		t.Fatalf("RedisBusinessTTL = %v, want 5m", cfg.RedisBusinessTTL)
	}
	if cfg.GoogleTokenInfoURL != "https://oauth2.googleapis.com/tokeninfo" {
		t.Fatalf("GoogleTokenInfoURL = %q", cfg.GoogleTokenInfoURL)
	}
	if cfg.PaystackBaseURL != "https://api.paystack.co" {
		t.Fatalf("PaystackBaseURL = %q", cfg.PaystackBaseURL)
	}
	// Secrets have no default: they must come from the environment.
	if cfg.GoogleClientID != "" || cfg.PaystackSecretKey != "" {
		t.Fatal("a credential was given a default value")
	}
	// Every outbound call must be bounded.
	if cfg.GoogleTimeout <= 0 || cfg.PaystackTimeout <= 0 {
		t.Fatalf("timeouts must be positive: google=%v paystack=%v", cfg.GoogleTimeout, cfg.PaystackTimeout)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	isolate(t)
	t.Setenv("PORT", "9090")
	t.Setenv("REDIS_ADDR", "localhost:6380")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("REDIS_BUSINESS_TTL_SECONDS", "45")
	t.Setenv("GOOGLE_CLIENT_ID", "client-id.apps.googleusercontent.com")
	t.Setenv("PAYSTACK_SECRET_KEY", "sk_test_x")
	t.Setenv("PAYSTACK_HTTP_TIMEOUT_SECONDS", "7")

	cfg := config.Load()
	if cfg.Port != "9090" || cfg.RedisAddr != "localhost:6380" || cfg.RedisDB != 3 {
		t.Fatalf("unexpected %+v", cfg)
	}
	if cfg.RedisBusinessTTL != 45*time.Second {
		t.Fatalf("RedisBusinessTTL = %v, want 45s", cfg.RedisBusinessTTL)
	}
	if cfg.GoogleClientID != "client-id.apps.googleusercontent.com" || cfg.PaystackSecretKey != "sk_test_x" {
		t.Fatalf("credentials not read: %+v", cfg)
	}
	if cfg.PaystackTimeout != 7*time.Second {
		t.Fatalf("PaystackTimeout = %v, want 7s", cfg.PaystackTimeout)
	}
}

// A malformed number must fall back to the default rather than silently
// becoming zero — a zero TTL or timeout is materially different behaviour.
func TestLoadIgnoresMalformedNumbers(t *testing.T) {
	isolate(t)
	t.Setenv("REDIS_DB", "not-a-number")
	t.Setenv("REDIS_BUSINESS_TTL_SECONDS", "")
	t.Setenv("PAYSTACK_HTTP_TIMEOUT_SECONDS", "abc")

	cfg := config.Load()
	if cfg.RedisDB != 0 {
		t.Fatalf("RedisDB = %d, want the default 0", cfg.RedisDB)
	}
	if cfg.RedisBusinessTTL != 5*time.Minute {
		t.Fatalf("RedisBusinessTTL = %v, want the 5m default", cfg.RedisBusinessTTL)
	}
	if cfg.PaystackTimeout != 5*time.Second {
		t.Fatalf("PaystackTimeout = %v, want the 5s default", cfg.PaystackTimeout)
	}
}

func TestLoadReadsDotEnvWithoutOverridingRealEnvironment(t *testing.T) {
	dir := isolate(t)
	dotenv := `# a comment
PORT=7000

REDIS_ADDR="localhost:6399"
PAYSTACK_SECRET_KEY='sk_test_from_dotenv'
GOOGLE_CLIENT_ID=dotenv.apps.googleusercontent.com
MALFORMED_LINE
`
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	// A real environment variable must win over the file.
	t.Setenv("PORT", "9999")

	cfg := config.Load()
	if cfg.Port != "9999" {
		t.Fatalf("Port = %q; .env overrode the real environment", cfg.Port)
	}
	if cfg.RedisAddr != "localhost:6399" {
		t.Fatalf("RedisAddr = %q; quoted .env value not parsed", cfg.RedisAddr)
	}
	if cfg.PaystackSecretKey != "sk_test_from_dotenv" {
		t.Fatalf("PaystackSecretKey = %q; single-quoted value not parsed", cfg.PaystackSecretKey)
	}
	if cfg.GoogleClientID != "dotenv.apps.googleusercontent.com" {
		t.Fatalf("GoogleClientID = %q", cfg.GoogleClientID)
	}
}

// A missing .env is the normal case in a deployed environment.
func TestLoadWithoutDotEnvDoesNotFail(t *testing.T) {
	isolate(t)
	if cfg := config.Load(); cfg.Port != "8080" {
		t.Fatalf("Port = %q", cfg.Port)
	}
}
