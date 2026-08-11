package main

import (
	"os"
	"time"

	"github.com/maoni/backend-takehome/internal/api"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

func main() {
	st := store.NewMemoryStore()
	google := &auth.GoogleVerifier{ClientID: os.Getenv("GOOGLE_CLIENT_ID"), TokenInfoURL: env("GOOGLE_TOKENINFO_URL", "https://oauth2.googleapis.com/tokeninfo")}
	paystack := &payments.PaystackClient{SecretKey: os.Getenv("PAYSTACK_SECRET_KEY"), BaseURL: env("PAYSTACK_BASE_URL", "https://api.paystack.co")}
	srv := api.New(&service.BusinessService{Store: st}, &service.AuthService{Store: st, Verifier: google}, &service.SubscriptionService{Store: st, Provider: paystack})
	srv.Echo.Server.ReadHeaderTimeout = 5 * time.Second
	srv.Echo.Logger.Fatal(srv.Echo.Start(":" + env("PORT", "8080")))
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
