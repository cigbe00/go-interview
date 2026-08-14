package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

type Server struct {
	Echo          *echo.Echo
	Businesses    *service.BusinessService
	Auth          *service.AuthService
	Subscriptions *service.SubscriptionService
	RedisHealthy  func() bool
}

func New(b *service.BusinessService, a *service.AuthService, s *service.SubscriptionService, redisHealthy func() bool) *Server {
	e := echo.New()
	srv := &Server{Echo: e, Businesses: b, Auth: a, Subscriptions: s, RedisHealthy: redisHealthy}
	srv.routes()
	return srv
}
func (s *Server) routes() {
	s.Echo.GET("/health", func(c echo.Context) error {
		healthy := false
		if s.RedisHealthy != nil {
			healthy = s.RedisHealthy()
		}
		return c.JSON(http.StatusOK, map[string]any{"status": "ok", "redis": healthy})
	})
	g := s.Echo.Group("/api/v1")
	g.GET("/businesses/:id", s.getBusiness)
	g.GET("/businesses/:id/reviews", s.listReviews)
	g.POST("/businesses/:id/reviews", s.createReview)
	g.POST("/auth/google", s.googleAuth)
	g.POST("/subscriptions/initialize", s.initializeSubscription)
	g.POST("/subscriptions/webhook", s.webhook)
	g.GET("/subscriptions/:userID", s.getSubscription)
}
func (s *Server) getBusiness(c echo.Context) error {
	b, err := s.Businesses.GetBusiness(c.Request().Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(404, map[string]string{"error": "business not found"})
	}
	if err != nil {
		return c.JSON(500, map[string]string{"error": "internal error"})
	}
	return c.JSON(200, b)
}
func (s *Server) listReviews(c echo.Context) error {
	page, _ := strconv.Atoi(c.QueryParam("page"))
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	return c.JSON(200, map[string]any{"data": s.Businesses.ListReviews(c.Param("id"), page, limit)})
}
func (s *Server) createReview(c echo.Context) error {
	var req struct {
		UserID string `json:"user_id"`
		Rating int    `json:"rating"`
		Body   string `json:"body"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request"})
	}
	r, err := s.Businesses.CreateReview(c.Request().Context(), c.Param("id"), req.UserID, req.Rating, req.Body)
	if errors.Is(err, service.ErrInvalidRating) {
		return c.JSON(400, map[string]string{"error": err.Error()})
	}
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(404, map[string]string{"error": "business not found"})
	}
	if err != nil {
		return c.JSON(500, map[string]string{"error": "internal error"})
	}
	return c.JSON(201, r)
}
func (s *Server) googleAuth(c echo.Context) error {
	var req struct {
		IDToken string `json:"id_token"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request"})
	}
	u, err := s.Auth.SignInGoogle(c.Request().Context(), req.IDToken)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrProviderUnavailable) {
			status = http.StatusBadGateway
		}
		return c.JSON(status, map[string]string{"error": err.Error()})
	}
	return c.JSON(200, map[string]any{"user": u})
}
func (s *Server) initializeSubscription(c echo.Context) error {
	var req struct {
		UserID   string `json:"user_id"`
		Email    string `json:"email"`
		PlanCode string `json:"plan_code"`
		Amount   int64  `json:"amount"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request"})
	}
	resp, err := s.Subscriptions.Initialize(c.Request().Context(), req.UserID, req.Email, req.PlanCode, req.Amount)
	if errors.Is(err, service.ErrInvalidSubscriptionRequest) {
		return c.JSON(400, map[string]string{"error": err.Error()})
	}
	if err != nil {
		return c.JSON(502, map[string]string{"error": err.Error()})
	}
	return c.JSON(200, resp)
}
func (s *Server) webhook(c echo.Context) error {
	r := c.Request()

	maxReader := http.MaxBytesReader(c.Response().Writer, r.Body, 1<<20)
	body, err := io.ReadAll(maxReader)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "could not read body"})
	}

	if err := s.Subscriptions.HandleWebhook(c.Request().Context(), body, c.Request().Header.Get("x-paystack-signature")); err != nil {
		if errors.Is(err, payments.ErrInvalidSignature) {
			return c.JSON(401, map[string]string{"error": "invalid signature"})
		}
		return c.JSON(400, map[string]string{"error": err.Error()})
	}
	return c.NoContent(200)
}
func (s *Server) getSubscription(c echo.Context) error {
	sub, err := s.Subscriptions.Store.GetSubscription(c.Param("userID"))
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(404, map[string]string{"error": "subscription not found"})
	}
	if err != nil {
		return c.JSON(500, map[string]string{"error": "internal error"})
	}
	return c.JSON(200, sub)
}
