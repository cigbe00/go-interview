package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/maoni/backend-takehome/internal/auth"
	"github.com/maoni/backend-takehome/internal/payments"
	"github.com/maoni/backend-takehome/internal/service"
	"github.com/maoni/backend-takehome/internal/store"
)

const maxRequestBodySize = 1 << 20

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
	businessID := c.Param("id")
	if businessID == "" {
		return c.JSON(400, map[string]string{"error": "business id is required"})
	}
	b, err := s.Businesses.GetBusiness(c.Request().Context(), businessID)
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
	businessID := c.Param("id")

	if businessID == "" {
		return c.JSON(400, map[string]string{"error": "business id is required"})
	}

	reviews, err := s.Businesses.ListReviews(businessID, page, limit)
	if errors.Is(err, store.ErrNotFound) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "business not found"})
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
	return c.JSON(http.StatusOK, map[string]any{"data": reviews})
}

func (s *Server) createReview(c echo.Context) error {
	var req struct {
		UserID string `json:"user_id"`
		Rating int    `json:"rating"`
		Body   string `json:"body"`
	}

	businessID := c.Param("id")
	if businessID == "" {
		return c.JSON(400, map[string]string{"error": "business id is required"})
	}

	if err := decodeJSON(c, &req); err != nil {
		return c.JSON(400, map[string]string{"error": "invalid request"})
	}
	r, err := s.Businesses.CreateReview(c.Request().Context(), businessID, req.UserID, req.Rating, req.Body)
	if errors.Is(err, service.ErrInvalidRating) || errors.Is(err, service.ErrUserIDMissing) {
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
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, auth.ErrInvalidToken), errors.Is(err, service.ErrEmailMissing), errors.Is(err, service.ErrSubjectMissing):
			status = http.StatusUnauthorized
		case errors.Is(err, store.ErrIdentityConflict):
			status = http.StatusConflict
		case errors.Is(err, auth.ErrProviderUnavailable):
			status = http.StatusBadGateway
		}
		return c.JSON(status, map[string]string{"error": http.StatusText(status)})
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
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, maxRequestBodySize))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		}
		return c.JSON(400, map[string]string{"error": "could not read body"})
	}
	if err := s.Subscriptions.HandleWebhook(c.Request().Context(), body, c.Request().Header.Get("x-paystack-signature")); err != nil {
		if errors.Is(err, payments.ErrInvalidSignature) {
			log.Printf("rejected payment webhook with invalid signature")
			return c.JSON(401, map[string]string{"error": "invalid signature"})
		}
		if errors.Is(err, service.ErrSubscriptionCorrelation) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "webhook does not match subscription"})
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

func decodeJSON(c echo.Context, destination any) error {
	body := http.MaxBytesReader(c.Response(), c.Request().Body, maxRequestBodySize)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}
