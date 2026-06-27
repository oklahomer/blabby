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
		{"known invalid email", "INVALID_EMAIL", "ignored", "Enter a valid email address"},
		{"known weak password", "WEAK_PASSWORD", "ignored", "Choose a stronger password"},
		{"known internal error", "INTERNAL_ERROR", "ignored", "Server error — please try again"},
		{"known service unavailable", "SERVICE_UNAVAILABLE", "ignored", "Server unavailable — please try again"},
		{"known verification rate limited", "VERIFICATION_RATE_LIMITED", "ignored", "Too many verification attempts — please wait and try again"},
		{"known room already member", "ROOM_ALREADY_MEMBER", "ignored", "Already joined this room"},
		{"known room not found", "ROOM_NOT_FOUND", "ignored", "Room not found"},
		{"known room not member", "ROOM_NOT_MEMBER", "ignored", "Not a member of this room"},
		{"known email already registered", "EMAIL_ALREADY_REGISTERED", "ignored", "Email already registered"},
		{"known handle already taken", "HANDLE_ALREADY_TAKEN", "ignored", "Handle already taken"},
		{"known invalid verification", "VERIFICATION_INVALID", "ignored", "Verification code is invalid or expired"},
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
