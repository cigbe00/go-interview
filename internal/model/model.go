package model

import "time"

type Business struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	ReviewCount int     `json:"review_count"`
	Average     float64 `json:"average_rating"`
}

type Review struct {
	ID         string    `json:"id"`
	BusinessID string    `json:"business_id"`
	UserID     string    `json:"user_id"`
	Rating     int       `json:"rating"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type User struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	GoogleID string `json:"google_id,omitempty"`
}

type Subscription struct {
	UserID      string    `json:"user_id"`
	Status      string    `json:"status"`
	Reference   string    `json:"reference"`
	PlanCode    string    `json:"plan_code"`
	LastEventID string    `json:"last_event_id,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}
