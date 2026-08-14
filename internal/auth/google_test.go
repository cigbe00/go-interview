package auth_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maoni/backend-takehome/internal/auth"
)

func TestGoogleVerifier_Verify(t *testing.T) {
	const validClientID = "test-client-id.apps.googleusercontent.com"

	tests := []struct {
		name           string
		idToken        string
		serverResponse string
		serverStatus   int
		wantErr        error
		wantSub        string
		wantEmail      string
	}{
		{
			name:    "Successful Verification",
			idToken: "valid_token",
			serverResponse: `{
				"iss": "https://accounts.google.com",
				"aud": "` + validClientID + `",
				"sub": "10987654321",
				"email": "user@example.com",
				"email_verified": "true",
				"name": "Alex Doe"
			}`,
			serverStatus: http.StatusOK,
			wantErr:      nil,
			wantSub:      "10987654321",
			wantEmail:    "user@example.com",
		},
		{
			name:           "Empty Token Rejected",
			idToken:        "   ",
			serverResponse: `{}`,
			serverStatus:   http.StatusOK,
			wantErr:        auth.ErrInvalidToken,
		},
		{
			name:    "Audience Mismatch Rejected",
			idToken: "token_wrong_aud",
			serverResponse: `{
				"iss": "https://accounts.google.com",
				"aud": "different-client-id",
				"sub": "10987654321",
				"email": "user@example.com",
				"email_verified": true
			}`,
			serverStatus: http.StatusOK,
			wantErr:      auth.ErrInvalidAudience,
		},
		{
			name:    "Unverified Email Rejected",
			idToken: "token_unverified",
			serverResponse: `{
				"iss": "https://accounts.google.com",
				"aud": "` + validClientID + `",
				"sub": "10987654321",
				"email": "unverified@example.com",
				"email_verified": false
			}`,
			serverStatus: http.StatusOK,
			wantErr:      auth.ErrUnverifiedEmail,
		},
		{
			name:           "400 Bad Request Returns ErrInvalidToken",
			idToken:        "bad_token",
			serverResponse: `{"error_description": "Invalid Value"}`,
			serverStatus:   http.StatusBadRequest,
			wantErr:        auth.ErrInvalidToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.serverStatus)
				fmt.Fprintln(w, tt.serverResponse)
			}))
			defer server.Close()

			verifier := auth.NewGoogleVerifier(validClientID, server.URL, server.Client())
			identity, err := verifier.Verify(context.Background(), tt.idToken)

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if identity.Subject != tt.wantSub {
				t.Errorf("expected Subject %s, got %s", tt.wantSub, identity.Subject)
			}
			if identity.Email != tt.wantEmail {
				t.Errorf("expected Email %s, got %s", tt.wantEmail, identity.Email)
			}
		})
	}
}
