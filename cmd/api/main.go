package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	cachepkg "github.com/maoni/backend-takehome/internal/cache"
	"github.com/maoni/backend-takehome/internal/config"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/rediscache"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	st := store.NewMemoryStore()

	var businessCache cachepkg.BusinessCache = cachepkg.NoopBusinessCache{}
	rc := rediscache.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	redisConnected := false
	pingCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	err := rc.Ping(pingCtx)
	cancel()
	if err != nil {
		logger.Warn("redis unavailable; continuing without cache", "addr", cfg.RedisAddr, "error", err)
		_ = rc.Close()
	} else {
		redisConnected = true
		businessCache = rc
		defer rc.Close()
		logger.Info("connected to local redis", "addr", cfg.RedisAddr)
	}

	redisHealthy := func() bool {
		if !redisConnected {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		return rc.Ping(ctx) == nil
	}

	// Separate clients so one provider's timeout budget cannot be tightened or
	// loosened by the other's configuration.
	google := &auth.GoogleVerifier{
		ClientID:     cfg.GoogleClientID,
		TokenInfoURL: cfg.GoogleTokenInfoURL,
		HTTPClient:   &http.Client{Timeout: cfg.GoogleTimeout},
	}
	paystack := &payments.PaystackClient{
		SecretKey:  cfg.PaystackSecretKey,
		BaseURL:    cfg.PaystackBaseURL,
		HTTPClient: &http.Client{Timeout: cfg.PaystackTimeout},
	}
	if cfg.GoogleClientID == "" {
		logger.Warn("GOOGLE_CLIENT_ID is not set; google sign-in will return 502 until it is configured")
	}
	if cfg.PaystackSecretKey == "" {
		logger.Warn("PAYSTACK_SECRET_KEY is not set; subscription initialize and webhooks will be rejected")
	}

	srv := api.New(
		&service.BusinessService{Store: st, Cache: businessCache, CacheTTL: cfg.RedisBusinessTTL, Logger: logger},
		&service.AuthService{Store: st, Verifier: google},
		&service.SubscriptionService{Store: st, Provider: paystack, Logger: logger},
		redisHealthy,
	)
	srv.Logger = logger
	srv.Echo.Server.ReadHeaderTimeout = 5 * time.Second
	srv.Echo.Server.ReadTimeout = 10 * time.Second
	srv.Echo.Server.WriteTimeout = 10 * time.Second

	// Drain in-flight requests on SIGINT/SIGTERM instead of cutting them off:
	// a webhook that is mid-apply should be allowed to finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("maoni take-home api listening", "port", cfg.Port)
		if err := srv.Echo.Start(":" + cfg.Port); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := srv.Echo.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
	}
	logger.Info("shutdown complete")
}
