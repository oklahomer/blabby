package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// DefaultRegistrationTimeout bounds each registration-flow HTTP round-trip
// (register, verify, resend).
const DefaultRegistrationTimeout = 5 * time.Second

// Outbound tea.Msg types emitted by RegisterCmd. Each Cmd in this file emits
// exactly one message, following the login triple: Succeeded for the happy
// path, Rejected for a server error envelope, TransportError for a
// network-level failure before any response arrived.

// RegisterSucceeded reports a 201 from POST /users: a pending account exists
// (fresh or re-registered) and a verification PIN is on its way to Email.
type RegisterSucceeded struct {
	Email string
}

// RegisterRejected reports an error envelope from POST /users.
type RegisterRejected struct {
	Status     string
	Message    string
	HTTPStatus int
}

// RegisterTransportError reports a network-level registration failure.
type RegisterTransportError struct {
	Err error
}

// VerifySucceeded reports a 200 from POST /users/verifications: the account
// identified by Email is now active and can sign in.
type VerifySucceeded struct {
	Email string
}

// VerifyRejected reports an error envelope from POST /users/verifications.
type VerifyRejected struct {
	Status     string
	Message    string
	HTTPStatus int
}

// VerifyTransportError reports a network-level verification failure.
type VerifyTransportError struct {
	Err error
}

// ResendSucceeded reports a 200 from POST /users/verifications/resend: a
// fresh PIN is on its way (or the address was unknown — the server keeps that
// indistinguishable by design).
type ResendSucceeded struct{}

// ResendRejected reports an error envelope from POST /users/verifications/resend
// (in practice the 429 resend budget).
type ResendRejected struct {
	Status     string
	Message    string
	HTTPStatus int
}

// ResendTransportError reports a network-level resend failure.
type ResendTransportError struct {
	Err error
}

// RegisterCmd performs POST {server}/users and emits exactly one outbound
// tea.Msg describing the outcome. The password never appears in any returned
// message.
func RegisterCmd(client *http.Client, server, email, handle, password string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		payload := RegisterRequest{MailAddress: email, Handle: handle, Password: password}
		outcome, err := postRegistrationJSON(client, server, "/users", payload, timeout)
		if err != nil {
			return RegisterTransportError{Err: err}
		}
		if outcome.httpStatus == http.StatusCreated {
			return RegisterSucceeded{Email: strings.TrimSpace(email)}
		}
		return RegisterRejected{Status: outcome.status, Message: outcome.message, HTTPStatus: outcome.httpStatus}
	}
}

// VerifyEmailCmd performs POST {server}/users/verifications and emits exactly
// one outbound tea.Msg describing the outcome.
func VerifyEmailCmd(client *http.Client, server, email, pin string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		payload := VerifyRequest{MailAddress: email, PIN: pin}
		outcome, err := postRegistrationJSON(client, server, "/users/verifications", payload, timeout)
		if err != nil {
			return VerifyTransportError{Err: err}
		}
		if outcome.httpStatus == http.StatusOK {
			return VerifySucceeded{Email: strings.TrimSpace(email)}
		}
		return VerifyRejected{Status: outcome.status, Message: outcome.message, HTTPStatus: outcome.httpStatus}
	}
}

// ResendVerificationCmd performs POST {server}/users/verifications/resend and
// emits exactly one outbound tea.Msg describing the outcome.
func ResendVerificationCmd(client *http.Client, server, email string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		payload := ResendVerificationRequest{MailAddress: email}
		outcome, err := postRegistrationJSON(client, server, "/users/verifications/resend", payload, timeout)
		if err != nil {
			return ResendTransportError{Err: err}
		}
		if outcome.httpStatus == http.StatusOK {
			return ResendSucceeded{}
		}
		return ResendRejected{Status: outcome.status, Message: outcome.message, HTTPStatus: outcome.httpStatus}
	}
}

// registrationOutcome is a decoded non-transport response: the HTTP status
// plus, for error responses, the envelope's status/message (with the LoginCmd
// fallback of an empty status and a "server returned …" message when the
// envelope does not parse).
type registrationOutcome struct {
	httpStatus int
	status     string
	message    string
}

// postRegistrationJSON performs one JSON POST for the registration flow and
// classifies the result: a returned error is a transport-level failure; any
// server response becomes a registrationOutcome. The success bodies carry
// nothing the flow needs (the verify step keys on the email the user typed),
// so they are not decoded.
func postRegistrationJSON(client *http.Client, server, path string, payload any, timeout time.Duration) (registrationOutcome, error) {
	if timeout <= 0 {
		timeout = DefaultRegistrationTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	body, err := json.Marshal(payload)
	if err != nil {
		return registrationOutcome{}, fmt.Errorf("encode request: %w", err)
	}
	endpoint := strings.TrimRight(server, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return registrationOutcome{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return registrationOutcome{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, defaultReadLimitBytes))
	if err != nil {
		return registrationOutcome{}, fmt.Errorf("read response: %w", err)
	}

	outcome := registrationOutcome{httpStatus: resp.StatusCode}
	if resp.StatusCode >= http.StatusBadRequest {
		var env ErrorEnvelope
		if err := json.Unmarshal(raw, &env); err != nil || env.Error.Status == "" {
			outcome.message = fmt.Sprintf("server returned %s", resp.Status)
		} else {
			outcome.status = env.Error.Status
			outcome.message = env.Error.Message
		}
	}
	return outcome, nil
}
