package main

import (
	"context"
	"log"
	"net/http"
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
	cfg := config.Load()
	st := store.NewMemoryStore()

	var businessCache cachepkg.BusinessCache = cachepkg.NoopBusinessCache{}
	rc := rediscache.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	redisConnected := false
	pingCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	err := rc.Ping(pingCtx)
	cancel()
	if err != nil {
		log.Printf("redis unavailable at %s; continuing without cache: %v", cfg.RedisAddr, err)
		_ = rc.Close()
	} else {
		redisConnected = true
		businessCache = rc
		defer rc.Close()
		log.Printf("connected to local redis at %s", cfg.RedisAddr)
	}

	redisHealthy := func() bool {
		if !redisConnected {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		return rc.Ping(ctx) == nil
	}

	httpClient := &http.Client{Timeout: cfg.PaystackTimeout}
	google := &auth.GoogleVerifier{ClientID: cfg.GoogleClientID, TokenInfoURL: cfg.GoogleTokenInfoURL, HTTPClient: httpClient}
	paystack := &payments.PaystackClient{SecretKey: cfg.PaystackSecretKey, BaseURL: cfg.PaystackBaseURL, HTTPClient: httpClient}

	srv := api.New(
		&service.BusinessService{Store: st, Cache: businessCache, CacheTTL: cfg.RedisBusinessTTL},
		&service.AuthService{Store: st, Verifier: google},
		&service.SubscriptionService{Store: st, Provider: paystack},
		redisHealthy,
	)
	srv.Echo.Server.ReadHeaderTimeout = 5 * time.Second
	srv.Echo.Server.ReadTimeout = 10 * time.Second
	srv.Echo.Server.WriteTimeout = 10 * time.Second

	log.Printf("Maoni take-home API listening on :%s", cfg.Port)
	srv.Echo.Logger.Fatal(srv.Echo.Start(":" + cfg.Port))
}
