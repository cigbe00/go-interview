package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port               string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	RedisBusinessTTL   time.Duration
	GoogleClientID     string
	GoogleTokenInfoURL string
	PaystackSecretKey  string
	PaystackBaseURL    string
	PaystackTimeout    time.Duration
}

func Load() Config {
	_ = loadDotEnv(".env")
	return Config{
		Port:               env("PORT", "8080"),
		RedisAddr:          env("REDIS_ADDR", "localhost:6379"),
		RedisPassword:      os.Getenv("REDIS_PASSWORD"),
		RedisDB:            envInt("REDIS_DB", 0),
		RedisBusinessTTL:   time.Duration(envInt("REDIS_BUSINESS_TTL_SECONDS", 300)) * time.Second,
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleTokenInfoURL: env("GOOGLE_TOKENINFO_URL", "https://oauth2.googleapis.com/tokeninfo"),
		PaystackSecretKey:  os.Getenv("PAYSTACK_SECRET_KEY"),
		PaystackBaseURL:    env("PAYSTACK_BASE_URL", "https://api.paystack.co"),
		PaystackTimeout:    time.Duration(envInt("PAYSTACK_HTTP_TIMEOUT_SECONDS", 5)) * time.Second,
	}
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return d
	}
	return n
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" {
			if _, exists := os.LookupEnv(k); !exists {
				_ = os.Setenv(k, v)
			}
		}
	}
	return s.Err()
}
