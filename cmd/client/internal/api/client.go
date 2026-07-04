package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
)

// Default timeouts applied by the root Model when dispatching the
// transport Cmds. Exposed as variables so the integration test can
// shorten them without reaching into command bodies.
const (
	DefaultLoginTimeout   = 5 * time.Second
	DefaultWSDialTimeout  = 5 * time.Second
	DefaultWSAuthTimeout  = 6 * time.Second
	defaultReadLimitBytes = 1 << 20 // 1 MB cap on a single HTTP login response body
	// defaultMaxInboundFrame is the WebSocket read-limit the client
	// imposes for defence-in-depth. Sized at 1 MiB so it sits well
	// above the server's 64 KiB inbound cap; the client is bounding
	// what the server might send back, not enforcing the server's
	// inbound rule.
	defaultMaxInboundFrame = 1 << 20
	// closeWriteTimeout bounds how long the client waits while
	// emitting the WebSocket close frame on shutdown.
	closeWriteTimeout = time.Second
)

// Outbound tea.Msg types emitted by LoginCmd.

// LoginSucceeded carries the JWT and the trimmed email from a
// successful POST /login response. The email is mirrored back to
// the app so the Profile pane can show the credential the user typed.
type LoginSucceeded struct {
	Token string
	Email string
}

// StatusAccountPending is the login rejection status for a correct password
// against an account that has not completed email verification. The root
// Model routes it to the verify modal instead of rendering it as an error.
const StatusAccountPending = "AUTH_ACCOUNT_PENDING"

// LoginRejected reports that the server returned a parseable error
// envelope. Status is the gateway's status string (e.g.
// AUTH_INVALID_TOKEN); Message is the server-supplied fallback text
// used when the status is unknown to the client. Email echoes the
// attempted address so the pending-account route can hand it to the
// verify modal.
type LoginRejected struct {
	Status     string
	Message    string
	HTTPStatus int
	Email      string
}

// LoginTransportError reports a network-level failure before the
// server could send a response (DNS, connection refused, TLS error,
// timeout). Err is preserved verbatim so the login modal can surface
// the underlying reason in parentheses.
type LoginTransportError struct {
	Err error
}

// LoginProtocolError reports that the server responded but not with
// the login protocol: a 200 whose body is not a login response, or a
// body exceeding the read cap. Split from LoginTransportError so the
// modal does not render a misleading "Cannot reach server" for a
// server that was reached and misbehaved.
type LoginProtocolError struct {
	Err error
}

// Outbound tea.Msg types emitted by DialAndAuthCmd.

// SessionGeneration identifies the login session that started an
// asynchronous command. App Update drops completions whose generation
// no longer matches the current session.
type SessionGeneration uint64

// DialAndAuthRequest groups the inputs needed to open and authenticate
// a WebSocket connection for one login session.
type DialAndAuthRequest struct {
	Server      string
	Token       string
	Generation  SessionGeneration
	DialTimeout time.Duration
	AuthTimeout time.Duration
}

// WSAuthSucceeded carries the open websocket connection and the
// decoded user-id once the server's auth_ok frame is received.
type WSAuthSucceeded struct {
	Conn       *websocket.Conn
	UserID     string
	Generation SessionGeneration
}

// WSAuthRejected reports an auth_error frame from the server.
type WSAuthRejected struct {
	Status     string
	Message    string
	Generation SessionGeneration
}

// WSDialFailed reports a failure during HTTP upgrade or any IO issue
// before the first server frame arrives.
type WSDialFailed struct {
	Err        error
	Generation SessionGeneration
}

// WSAuthTimedOut reports that the auth_ok / auth_error frame did not
// arrive before the per-attempt deadline.
type WSAuthTimedOut struct {
	Generation SessionGeneration
}

// Outbound tea.Msg types emitted by ReadLoopCmd.

// WSFrameReceived carries the type tag of an inbound frame and its
// raw bytes for downstream decoding. The root Model currently drops
// every frame type silently; frame handlers will register as
// individual panes grow real state.
type WSFrameReceived struct {
	Type       string
	Raw        []byte
	Generation SessionGeneration
}

// WSDisconnected reports that the read loop has exited.
type WSDisconnected struct {
	Err        error
	Generation SessionGeneration
}

// LoginCmd performs POST {server}/login and emits exactly one
// outbound tea.Msg describing the outcome. The Cmd runs off the
// Bubble Tea Update goroutine; the bounded context applies to the
// entire round-trip including TLS handshake.
//
// The token never appears in any returned message except
// LoginSucceeded; it is never logged or echoed elsewhere.
func LoginCmd(client *http.Client, server, email, password string, timeout time.Duration) tea.Cmd {
	if timeout <= 0 {
		timeout = DefaultLoginTimeout
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		body, err := json.Marshal(LoginRequest{MailAddress: email, Password: password})
		if err != nil {
			return LoginTransportError{Err: fmt.Errorf("encode login request: %w", err)}
		}

		endpoint := strings.TrimRight(server, "/") + "/login"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return LoginTransportError{Err: fmt.Errorf("build login request: %w", err)}
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return LoginTransportError{Err: err}
		}
		defer func() { _ = resp.Body.Close() }()

		// Cap the body we'll read so a hostile or buggy server cannot
		// stall the TUI with an unbounded payload. MaxBytesReader (rather
		// than a silent LimitReader truncation) surfaces the overrun as a
		// typed error, classified as a protocol violation below.
		raw, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, defaultReadLimitBytes))
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				return LoginProtocolError{Err: fmt.Errorf("login response exceeds %d bytes", defaultReadLimitBytes)}
			}
			return LoginTransportError{Err: fmt.Errorf("read login response: %w", err)}
		}

		if resp.StatusCode == http.StatusOK {
			var lr LoginResponse
			if err := json.Unmarshal(raw, &lr); err != nil || lr.Token == "" {
				return LoginProtocolError{Err: errors.New("malformed login response from server")}
			}
			return LoginSucceeded{Token: lr.Token, Email: strings.TrimSpace(email)}
		}

		var env ErrorEnvelope
		if err := json.Unmarshal(raw, &env); err != nil || env.Error.Status == "" {
			return LoginRejected{
				Status:     "",
				Message:    fmt.Sprintf("server returned %s", resp.Status),
				HTTPStatus: resp.StatusCode,
				Email:      strings.TrimSpace(email),
			}
		}
		return LoginRejected{
			Status:     env.Error.Status,
			Message:    env.Error.Message,
			HTTPStatus: resp.StatusCode,
			Email:      strings.TrimSpace(email),
		}
	}
}

// DialAndAuthCmd opens a WebSocket to {server-as-ws}/ws, sends the
// auth frame, and waits for the first server frame (auth_ok or
// auth_error). On success the open connection is handed off to the
// root Model via WSAuthSucceeded; the caller is responsible for
// driving the read loop afterwards (see ReadLoopCmd).
//
// The token is enclosed inside the AuthFrame struct and never appears
// in any returned message.
func DialAndAuthCmd(req DialAndAuthRequest) tea.Cmd {
	if req.DialTimeout <= 0 {
		req.DialTimeout = DefaultWSDialTimeout
	}
	if req.AuthTimeout <= 0 {
		req.AuthTimeout = DefaultWSAuthTimeout
	}
	return func() tea.Msg {
		wsAddr, err := wsURL(req.Server)
		if err != nil {
			return WSDialFailed{Err: err, Generation: req.Generation}
		}

		dialer := &websocket.Dialer{HandshakeTimeout: req.DialTimeout}
		conn, _, err := dialer.Dial(wsAddr, nil)
		if err != nil {
			return WSDialFailed{Err: err, Generation: req.Generation}
		}
		conn.SetReadLimit(defaultMaxInboundFrame)

		auth := AuthFrame{Type: "auth", Token: req.Token}
		if err := conn.WriteJSON(auth); err != nil {
			_ = conn.Close()
			return WSDialFailed{Err: fmt.Errorf("send auth frame: %w", err), Generation: req.Generation}
		}

		if err := conn.SetReadDeadline(time.Now().Add(req.AuthTimeout)); err != nil {
			_ = conn.Close()
			return WSDialFailed{Err: fmt.Errorf("set auth read deadline: %w", err), Generation: req.Generation}
		}

		_, frame, err := conn.ReadMessage()
		if err != nil {
			_ = conn.Close()
			if netErr, ok := err.(interface{ Timeout() bool }); ok && netErr.Timeout() {
				return WSAuthTimedOut{Generation: req.Generation}
			}
			return WSDialFailed{Err: err, Generation: req.Generation}
		}

		// Clear the deadline so the steady-state read loop is not
		// bounded by the auth timeout.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			_ = conn.Close()
			return WSDialFailed{Err: fmt.Errorf("clear read deadline: %w", err), Generation: req.Generation}
		}

		var env FrameEnvelope
		if err := json.Unmarshal(frame, &env); err != nil {
			_ = conn.Close()
			return WSDialFailed{Err: fmt.Errorf("decode auth response: %w", err), Generation: req.Generation}
		}

		switch env.Type {
		case "auth_ok":
			userID, err := DecodeSub(req.Token)
			if err != nil {
				// The server already validated the token; an
				// undecodable sub claim is a strange but non-fatal
				// edge case. Hand the connection up regardless and
				// let the Profile pane fall back to an empty ID.
				userID = ""
			}
			return WSAuthSucceeded{Conn: conn, UserID: userID, Generation: req.Generation}
		case "auth_error":
			var ae AuthErrorFrame
			if err := json.Unmarshal(frame, &ae); err != nil {
				_ = conn.Close()
				return WSDialFailed{Err: fmt.Errorf("decode auth_error frame: %w", err), Generation: req.Generation}
			}
			_ = conn.Close()
			return WSAuthRejected{Status: ae.Status, Message: ae.Message, Generation: req.Generation}
		default:
			_ = conn.Close()
			return WSDialFailed{Err: fmt.Errorf("unexpected first frame type %q", env.Type), Generation: req.Generation}
		}
	}
}

// FrameSender is the subset of *tea.Program used by ReadLoopCmd to
// deliver inbound WebSocket frames back into the Update loop. Keeping
// this as an interface (rather than taking *tea.Program directly)
// makes the read-loop seam easy to test without spinning up a real
// program.
type FrameSender interface {
	Send(tea.Msg)
}

// ReadLoopRequest groups the inputs for a session-scoped WebSocket
// read loop.
type ReadLoopRequest struct {
	Context    context.Context
	Sender     FrameSender
	Conn       *websocket.Conn
	Generation SessionGeneration
}

// ReadLoopCmd starts a long-lived goroutine that drains the
// WebSocket and dispatches each frame back into the Update loop via
// FrameSender.Send. The Cmd itself returns nil immediately — the
// goroutine outlives the Cmd invocation and exits when the
// connection closes (cleanly or otherwise), at which point it sends
// a single WSDisconnected and returns.
//
// ctx is the program lifecycle context. When ctx is cancelled (e.g.,
// SIGTERM via signal.NotifyContext), the goroutine sets a read
// deadline of "now" on conn so the blocked ReadMessage returns
// immediately and the goroutine exits without waiting for the
// remote side to drop the connection.
//
// The goroutine is the sole owner of conn.Close: callers that want
// to terminate the loop should call CloseGracefully(conn) (which
// sends a normal-closure frame), or cancel ctx, or both. Either
// path unblocks ReadMessage and the deferred Close runs exactly once.
func ReadLoopCmd(req ReadLoopRequest) tea.Cmd {
	return func() tea.Msg {
		go func() {
			defer func() { _ = req.Conn.Close() }()

			// Watch ctx for cancellation. Forcing the read deadline
			// to "now" unblocks the ReadMessage below; gorilla
			// permits SetReadDeadline concurrently with reads, so
			// this is safe.
			watchDone := make(chan struct{})
			defer close(watchDone)
			go func() {
				select {
				case <-req.Context.Done():
					_ = req.Conn.SetReadDeadline(time.Now())
				case <-watchDone:
				}
			}()

			for {
				_, frame, err := req.Conn.ReadMessage()
				if err != nil {
					req.Sender.Send(WSDisconnected{Err: err, Generation: req.Generation})
					return
				}
				var env FrameEnvelope
				if jsonErr := json.Unmarshal(frame, &env); jsonErr != nil {
					// Malformed frame from the server is a server-side
					// bug; drop it silently rather than crashing the UI.
					continue
				}
				req.Sender.Send(WSFrameReceived{Type: env.Type, Raw: frame, Generation: req.Generation})
			}
		}()
		return nil
	}
}

// CloseGracefully sends a normal-closure (1000) frame on conn and
// returns. It does NOT call conn.Close() — the read loop's deferred
// Close runs after ReadMessage returns the close error. Calling
// Close from outside the read loop concurrently with ReadMessage is
// not safe per gorilla/websocket's documented single-writer /
// single-reader contract; CloseGracefully is the safe shutdown
// signal because WriteControl is the one method gorilla permits
// concurrently with reads.
//
// Safe to call on a nil conn (no-op).
func CloseGracefully(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	deadline := time.Now().Add(closeWriteTimeout)
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	_ = conn.WriteControl(websocket.CloseMessage, closeMsg, deadline)
}

// wsURL translates a parsed http(s) server URL to its WebSocket
// equivalent and appends /ws. http → ws, https → wss; any other
// scheme (including ws/wss themselves passed in by mistake) is
// rejected with a typed error so callers fail fast at the boundary.
func wsURL(server string) (string, error) {
	u, err := url.Parse(server)
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported scheme %q (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("server URL has no host")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
