package api

import (
	"encoding/json"
	"errors"
	"github.com/labstack/echo/v4"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxRequestBody = 1 << 20

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
	page, err := queryInt(c.QueryParam("page"), 1)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "page must be a positive integer"})
	}
	limit, err := queryInt(c.QueryParam("limit"), 10)
	if err != nil || limit > 100 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit must be between 1 and 100"})
	}
	reviews, err := s.Businesses.ListReviews(c.Param("id"), page, limit)
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "business not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": reviews, "page": page, "limit": limit})
}
func (s *Server) createReview(c echo.Context) error {
	var req struct {
		UserID string `json:"user_id"`
		Rating int    `json:"rating"`
		Body   string `json:"body"`
	}
	if err := decodeJSON(c, &req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request"})
	}
	r, err := s.Businesses.CreateReview(c.Request().Context(), c.Param("id"), req.UserID, req.Rating, req.Body)
	if errors.Is(err, service.ErrInvalidRating) || errors.Is(err, service.ErrInvalidReview) {
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
	if err := decodeJSON(c, &req); err != nil {
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
	if err := decodeJSON(c, &req); err != nil {
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
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, maxRequestBody)
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return c.JSON(400, map[string]string{"error": "could not read body"})
	}
	if err := s.Subscriptions.HandleWebhook(c.Request().Context(), body, c.Request().Header.Get("x-paystack-signature")); err != nil {
		if errors.Is(err, payments.ErrInvalidSignature) {
			return c.JSON(401, map[string]string{"error": "invalid signature"})
		}
		return c.JSON(400, map[string]string{"error": err.Error()})
	}
	return c.NoContent(200)
}

func decodeJSON(c echo.Context, dst any) error {
	if !strings.HasPrefix(strings.ToLower(c.Request().Header.Get(echo.HeaderContentType)), echo.MIMEApplicationJSON) {
		return errors.New("content type must be application/json")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, maxRequestBody)
	dec := json.NewDecoder(c.Request().Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func queryInt(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, errors.New("invalid positive integer")
	}
	return n, nil
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
