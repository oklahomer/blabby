package api

import "testing"

func TestHumanise(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		fallback string
		want     string
	}{
		{"known invalid token", "AUTH_INVALID_TOKEN", "ignored", "Invalid credentials"},
		{"known expired token", "AUTH_EXPIRED_TOKEN", "ignored", "Session expired — please sign in again"},
		{"known missing token", "AUTH_MISSING_TOKEN", "ignored", "Authentication timed out"},
		{"known invalid request", "INVALID_REQUEST", "ignored", "Invalid request"},
		{"known internal error", "INTERNAL_ERROR", "ignored", "Server error — please try again"},
		{"known service unavailable", "SERVICE_UNAVAILABLE", "ignored", "Server unavailable — please try again"},
		{"unknown status falls back to message", "FUTURE_NEW_STATUS", "server says: ouch", "server says: ouch"},
		{"unknown status with empty fallback", "ANOTHER_NEW_STATUS", "", "Unexpected error from server"},
		{"empty status with non-empty fallback", "", "the server is grumpy", "the server is grumpy"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Humanise(tc.status, tc.fallback)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
