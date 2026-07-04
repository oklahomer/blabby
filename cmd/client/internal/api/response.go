package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// readBoundedResponseBody caps response bodies from pre-login HTTP calls so a
// hostile or buggy server cannot stall the TUI with an unbounded payload.
func readBoundedResponseBody(body io.ReadCloser) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(nil, body, defaultReadLimitBytes))
}

// decodeErrorEnvelope extracts the gateway error envelope from an HTTP error
// response. Malformed envelopes still become a displayable rejection message so
// callers do not have to special-case broken servers.
func decodeErrorEnvelope(raw []byte, fallbackStatus string) (status, message string) {
	var env ErrorEnvelope
	if err := json.Unmarshal(raw, &env); err != nil || env.Error.Status == "" {
		return "", fmt.Sprintf("server returned %s", fallbackStatus)
	}
	return env.Error.Status, env.Error.Message
}
