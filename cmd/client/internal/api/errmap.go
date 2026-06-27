package api

// errorMessages maps gateway error.status values to UX strings.
// Unknown statuses fall through to the server's error.message string
// via Humanise, so a future server release that introduces a new
// status does not require a client update to render sensibly.
var errorMessages = map[string]string{
	"AUTH_INVALID_TOKEN":        "Invalid credentials",
	"AUTH_EXPIRED_TOKEN":        "Session expired — please sign in again",
	"AUTH_MISSING_TOKEN":        "Authentication timed out",
	"INVALID_REQUEST":           "Invalid request",
	"MISSING_FIELD":             "A required field is missing",
	"PAYLOAD_TOO_LARGE":         "Request too large",
	"INVALID_EMAIL":             "Enter a valid email address",
	"WEAK_PASSWORD":             "Choose a stronger password",
	"INTERNAL_ERROR":            "Server error — please try again",
	"SERVICE_UNAVAILABLE":       "Server unavailable — please try again",
	"RATE_LIMIT_EXCEEDED":       "Too many attempts — please wait and try again",
	"VERIFICATION_RATE_LIMITED": "Too many verification attempts — please wait and try again",
	"ROOM_ALREADY_MEMBER":       "Already joined this room",
	"ROOM_NOT_FOUND":            "Room not found",
	"ROOM_NOT_MEMBER":           "Not a member of this room",
	"EMAIL_ALREADY_REGISTERED":  "Email already registered",
	"HANDLE_ALREADY_TAKEN":      "Handle already taken",
	"VERIFICATION_INVALID":      "Verification code is invalid or expired",
}

// Humanise returns the UX-friendly message for the given gateway
// status, falling back to the server-supplied message (typically
// error.message) when the status is not in the table. If both are
// empty it returns a generic apology so the UI is never blank.
func Humanise(status, fallback string) string {
	if msg, ok := errorMessages[status]; ok {
		return msg
	}
	if fallback != "" {
		return fallback
	}
	return "Unexpected error from server"
}
