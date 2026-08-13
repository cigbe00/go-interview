package api

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

const (
	maxRequestBody = 1 << 20 // 1 MiB
	defaultPage    = 1
)

// Client-facing messages. Internal detail (provider responses, decode errors,
// which validation rule a token failed) is logged, never returned: it is of no
// use to a caller and tells an attacker how the checks work.
const (
	msgInternal            = "internal error"
	msgBusinessNotFound    = "business not found"
	msgInvalidRequest      = "invalid request"
	msgInvalidCredentials  = "invalid identity token"
	msgProviderUnavailable = "identity provider unavailable"
	msgPaymentProvider     = "payment provider error"
	msgInvalidSignature    = "invalid signature"
	msgWebhookRejected     = "webhook could not be processed"
	msgSubscriptionMissing = "subscription not found"
)

type Server struct {
	Echo          *echo.Echo
	Businesses    *service.BusinessService
	Auth          *service.AuthService
	Subscriptions *service.SubscriptionService
	RedisHealthy  func() bool
	Logger        *slog.Logger
}

func New(b *service.BusinessService, a *service.AuthService, s *service.SubscriptionService, redisHealthy func() bool) *Server {
	e := echo.New()
	e.HideBanner = true
	srv := &Server{Echo: e, Businesses: b, Auth: a, Subscriptions: s, RedisHealthy: redisHealthy, Logger: slog.Default()}
	srv.middleware()
	srv.routes()
	return srv
}

func (s *Server) middleware() {
	s.Echo.Use(middleware.Recover())
	s.Echo.Use(middleware.RequestID())
	s.Echo.Use(middleware.BodyLimit(strconv.Itoa(maxRequestBody)))
	// Keep framework-generated errors (404, 405, 415, body-limit) in the same
	// JSON envelope the handlers use, so clients only parse one shape.
	s.Echo.HTTPErrorHandler = s.errorHandler
}

func (s *Server) errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}
	status := http.StatusInternalServerError
	message := msgInternal
	var he *echo.HTTPError
	if errors.As(err, &he) {
		status = he.Code
		if m, ok := he.Message.(string); ok {
			message = m
		} else {
			message = http.StatusText(he.Code)
		}
	}
	if status >= http.StatusInternalServerError {
		s.logger().Error("request failed",
			"method", c.Request().Method, "path", c.Path(), "status", status, "error", err)
	}
	_ = c.JSON(status, errorBody(message))
}

func (s *Server) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func errorBody(msg string) map[string]string { return map[string]string{"error": msg} }

func (s *Server) routes() {
	s.Echo.GET("/health", s.health)
	g := s.Echo.Group("/api/v1")
	g.GET("/businesses/:id", s.getBusiness)
	g.GET("/businesses/:id/reviews", s.listReviews)
	g.POST("/businesses/:id/reviews", s.createReview)
	g.POST("/auth/google", s.googleAuth)
	g.POST("/subscriptions/initialize", s.initializeSubscription)
	g.POST("/subscriptions/webhook", s.webhook)
	g.GET("/subscriptions/:userID", s.getSubscription)
}

func (s *Server) health(c echo.Context) error {
	healthy := false
	if s.RedisHealthy != nil {
		healthy = s.RedisHealthy()
	}
	// Redis is a cache, not a dependency the API cannot serve without, so its
	// state is reported without failing the health check.
	return c.JSON(http.StatusOK, map[string]any{"status": "ok", "redis": healthy})
}

func (s *Server) getBusiness(c echo.Context) error {
	b, err := s.Businesses.GetBusiness(c.Request().Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errorBody(msgBusinessNotFound))
	}
	if err != nil {
		s.logger().ErrorContext(c.Request().Context(), "get business failed", "business_id", c.Param("id"), "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody(msgInternal))
	}
	return c.JSON(http.StatusOK, b)
}

func (s *Server) listReviews(c echo.Context) error {
	page, err := positiveIntParam(c.QueryParam("page"), defaultPage)
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("page must be a positive integer"))
	}
	limit, err := positiveIntParam(c.QueryParam("limit"), service.DefaultPageLimit)
	if err != nil || limit > service.MaxPageLimit {
		return c.JSON(http.StatusBadRequest, errorBody("limit must be an integer between 1 and "+strconv.Itoa(service.MaxPageLimit)))
	}

	reviews, total, err := s.Businesses.ListReviews(c.Request().Context(), c.Param("id"), page, limit)
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errorBody(msgBusinessNotFound))
	}
	if err != nil {
		s.logger().ErrorContext(c.Request().Context(), "list reviews failed", "business_id", c.Param("id"), "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody(msgInternal))
	}
	return c.JSON(http.StatusOK, map[string]any{
		"data":  reviews,
		"page":  page,
		"limit": limit,
		"total": total,
	})
}

// positiveIntParam parses an optional positive-integer query parameter.
// An absent parameter falls back to def; a present but unparseable or
// non-positive value is an error rather than being silently coerced.
func positiveIntParam(raw string, def int) (int, error) {
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if n < 1 {
		return 0, errors.New("must be positive")
	}
	return n, nil
}

func (s *Server) createReview(c echo.Context) error {
	var req struct {
		UserID string `json:"user_id"`
		Rating int    `json:"rating"`
		Body   string `json:"body"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody(msgInvalidRequest))
	}
	r, err := s.Businesses.CreateReview(c.Request().Context(), c.Param("id"), req.UserID, req.Rating, req.Body)
	switch {
	case errors.Is(err, service.ErrInvalidRating),
		errors.Is(err, service.ErrUserRequired),
		errors.Is(err, service.ErrBodyTooLong):
		return c.JSON(http.StatusBadRequest, errorBody(err.Error()))
	case errors.Is(err, store.ErrNotFound):
		return c.JSON(http.StatusNotFound, errorBody(msgBusinessNotFound))
	case err != nil:
		s.logger().ErrorContext(c.Request().Context(), "create review failed", "business_id", c.Param("id"), "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody(msgInternal))
	}
	return c.JSON(http.StatusCreated, r)
}

func (s *Server) googleAuth(c echo.Context) error {
	var req struct {
		IDToken string `json:"id_token"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody(msgInvalidRequest))
	}
	u, err := s.Auth.SignInGoogle(c.Request().Context(), req.IDToken)
	if err != nil {
		// A provider outage is our problem, not the caller's: it must not be
		// reported as a rejected credential.
		if errors.Is(err, auth.ErrProviderUnavailable) {
			s.logger().ErrorContext(c.Request().Context(), "google sign-in provider failure", "error", err)
			return c.JSON(http.StatusBadGateway, errorBody(msgProviderUnavailable))
		}
		// Which check the token failed (audience, issuer, expiry, verified
		// email) is logged for us, not returned to the caller.
		s.logger().InfoContext(c.Request().Context(), "google sign-in rejected", "error", err)
		return c.JSON(http.StatusUnauthorized, errorBody(msgInvalidCredentials))
	}
	return c.JSON(http.StatusOK, map[string]any{"user": u})
}

func (s *Server) initializeSubscription(c echo.Context) error {
	var req struct {
		UserID   string `json:"user_id"`
		Email    string `json:"email"`
		PlanCode string `json:"plan_code"`
		Amount   int64  `json:"amount"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, errorBody(msgInvalidRequest))
	}
	resp, err := s.Subscriptions.Initialize(c.Request().Context(), req.UserID, req.Email, req.PlanCode, req.Amount)
	if errors.Is(err, service.ErrInvalidSubscriptionRequest) {
		return c.JSON(http.StatusBadRequest, errorBody(err.Error()))
	}
	if err != nil {
		s.logger().ErrorContext(c.Request().Context(), "subscription initialize failed", "user_id", req.UserID, "error", err)
		return c.JSON(http.StatusBadGateway, errorBody(msgPaymentProvider))
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) webhook(c echo.Context) error {
	// The signature covers the exact bytes Paystack sent, so the raw body must
	// be read in full before anything else touches it. The previous single
	// Body.Read call trusted Content-Length and ignored short reads, which
	// silently truncated the payload and broke both parsing and signature
	// verification.
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, maxRequestBody))
	if err != nil {
		return c.JSON(http.StatusBadRequest, errorBody("could not read body"))
	}

	err = s.Subscriptions.HandleWebhook(c.Request().Context(), body, c.Request().Header.Get("x-paystack-signature"))
	switch {
	case errors.Is(err, payments.ErrInvalidSignature):
		s.logger().WarnContext(c.Request().Context(), "rejected webhook with invalid signature", "bytes", len(body))
		return c.JSON(http.StatusUnauthorized, errorBody(msgInvalidSignature))
	case errors.Is(err, service.ErrUnknownSubscription):
		// Deliberately not acknowledged. Money may have moved without a
		// subscription to apply it to, so Paystack should retry: the event was
		// not consumed, and a redelivery succeeds once the subscription
		// exists. This needs to alert — see PULL_REQUEST.md.
		s.logger().ErrorContext(c.Request().Context(), "webhook could not be matched to a subscription", "error", err)
		return c.JSON(http.StatusNotFound, errorBody(msgSubscriptionMissing))
	case errors.Is(err, payments.ErrUnsupportedEvent):
		// Acknowledged so the provider stops retrying an event we ignore.
		return c.NoContent(http.StatusOK)
	case err != nil:
		// Parse and provider detail is logged, not echoed back to the sender.
		s.logger().ErrorContext(c.Request().Context(), "webhook processing failed", "error", err)
		return c.JSON(http.StatusBadRequest, errorBody(msgWebhookRejected))
	}
	return c.NoContent(http.StatusOK)
}

func (s *Server) getSubscription(c echo.Context) error {
	sub, err := s.Subscriptions.Get(c.Param("userID"))
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(http.StatusNotFound, errorBody(msgSubscriptionMissing))
	}
	if err != nil {
		s.logger().ErrorContext(c.Request().Context(), "get subscription failed", "user_id", c.Param("userID"), "error", err)
		return c.JSON(http.StatusInternalServerError, errorBody(msgInternal))
	}
	return c.JSON(http.StatusOK, sub)
}
