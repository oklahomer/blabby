package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
)

func TestWSURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"http localhost", "http://localhost:8080", "ws://localhost:8080/ws", false},
		{"https remote", "https://chat.example.com", "wss://chat.example.com/ws", false},
		{"http with trailing slash", "http://localhost:8080/", "ws://localhost:8080/ws", false},
		{"http with path prefix", "http://example.com/api", "ws://example.com/api/ws", false},
		{"ws scheme rejected", "ws://localhost:8080", "", true},
		{"wss scheme rejected", "wss://example.com", "", true},
		{"empty host rejected", "http://", "", true},
		{"unparseable rejected", "://bad", "", true},
		{"ftp rejected", "ftp://example.com", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wsURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLoginCmdSuccess(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/login" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if req["mail_address"] != "rina@example.com" || req["password"] != "hunter2" {
			t.Errorf("got credentials %q/%q", req["mail_address"], req["password"])
		}
		if _, ok := req["username"]; ok {
			t.Errorf("login request still sent username field: %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(LoginResponse{Token: "fake.jwt.token"})
	}))
	defer srv.Close()

	msg := LoginCmd(srv.Client(), srv.URL, "rina@example.com", "hunter2", 2*time.Second)()
	got, ok := msg.(LoginSucceeded)
	if !ok {
		t.Fatalf("expected LoginSucceeded, got %T: %#v", msg, msg)
	}
	if got.Token != "fake.jwt.token" {
		t.Fatalf("got token %q", got.Token)
	}
	if got.Email != "rina@example.com" {
		t.Fatalf("got email %q", got.Email)
	}
}

func TestLoginCmdRejected(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 1001, Status: "AUTH_INVALID_TOKEN", Message: "invalid credentials",
		}})
	}))
	defer srv.Close()

	msg := LoginCmd(srv.Client(), srv.URL, "rina@example.com", "wrong", 2*time.Second)()
	got, ok := msg.(LoginRejected)
	if !ok {
		t.Fatalf("expected LoginRejected, got %T", msg)
	}
	if got.Status != "AUTH_INVALID_TOKEN" {
		t.Fatalf("got status %q", got.Status)
	}
	if got.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("got http status %d", got.HTTPStatus)
	}
}

func TestLoginCmdTransportError(t *testing.T) {
	t.Parallel()
	// Unreachable server (closed immediately).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	msg := LoginCmd(&http.Client{}, addr, "rina@example.com", "hunter2", 500*time.Millisecond)()
	if _, ok := msg.(LoginTransportError); !ok {
		t.Fatalf("expected LoginTransportError, got %T", msg)
	}
}

func TestLoginCmdMalformedSuccessResponse(t *testing.T) {
	t.Parallel()
	// The server responded, so this is a protocol violation, not a transport
	// failure — the modal must not render "Cannot reach server".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":""}`))
	}))
	defer srv.Close()

	msg := LoginCmd(srv.Client(), srv.URL, "rina@example.com", "hunter2", 2*time.Second)()
	if _, ok := msg.(LoginProtocolError); !ok {
		t.Fatalf("expected LoginProtocolError, got %T", msg)
	}
}

func TestLoginCmdOversizeResponseIsProtocolError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// One byte past the cap so the bounded reader trips.
		_, _ = w.Write(bytes.Repeat([]byte("a"), defaultReadLimitBytes+1))
	}))
	defer srv.Close()

	msg := LoginCmd(srv.Client(), srv.URL, "rina@example.com", "hunter2", 2*time.Second)()
	if _, ok := msg.(LoginProtocolError); !ok {
		t.Fatalf("expected LoginProtocolError for an oversize body, got %T", msg)
	}
}

// validJWT mints a JWT-shaped token whose sub claim is the given id.
func validJWT(t *testing.T, sub string) string {
	t.Helper()
	return mintToken(t, []byte(`{"sub":"`+sub+`"}`))
}

func TestDialAndAuthCmdSuccess(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read auth: %v", err)
			return
		}
		var af AuthFrame
		if err := json.Unmarshal(raw, &af); err != nil {
			t.Errorf("decode auth: %v", err)
			return
		}
		if af.Type != "auth" {
			t.Errorf("got auth type %q", af.Type)
		}
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
	}))
	defer srv.Close()

	httpURL := "http://" + srv.Listener.Addr().String()
	tok := validJWT(t, "u-rina")
	msg := DialAndAuthCmd(DialAndAuthRequest{
		Server:      httpURL,
		Token:       tok,
		Generation:  testGeneration,
		DialTimeout: time.Second,
		AuthTimeout: time.Second,
	})()
	got, ok := msg.(WSAuthSucceeded)
	if !ok {
		t.Fatalf("expected WSAuthSucceeded, got %T: %#v", msg, msg)
	}
	if got.UserID != "u-rina" {
		t.Fatalf("got user id %q", got.UserID)
	}
	if got.Conn == nil {
		t.Fatal("expected non-nil conn")
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
	_ = got.Conn.Close()
}

func TestDialAndAuthCmdRejected(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteJSON(AuthErrorFrame{
			Type: "auth_error", Code: 1001, Status: "AUTH_INVALID_TOKEN", Message: "invalid token",
		})
	}))
	defer srv.Close()

	httpURL := "http://" + srv.Listener.Addr().String()
	msg := DialAndAuthCmd(DialAndAuthRequest{
		Server:      httpURL,
		Token:       validJWT(t, "u-rina"),
		Generation:  testGeneration,
		DialTimeout: time.Second,
		AuthTimeout: time.Second,
	})()
	got, ok := msg.(WSAuthRejected)
	if !ok {
		t.Fatalf("expected WSAuthRejected, got %T", msg)
	}
	if got.Status != "AUTH_INVALID_TOKEN" {
		t.Fatalf("got status %q", got.Status)
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
}

func TestDialAndAuthCmdDialFails(t *testing.T) {
	t.Parallel()
	// Stand up and immediately close so the dial fails fast.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close()

	msg := DialAndAuthCmd(DialAndAuthRequest{
		Server:      "http://" + addr,
		Token:       validJWT(t, "u-rina"),
		Generation:  testGeneration,
		DialTimeout: 250 * time.Millisecond,
		AuthTimeout: 250 * time.Millisecond,
	})()
	got, ok := msg.(WSDialFailed)
	if !ok {
		t.Fatalf("expected WSDialFailed, got %T", msg)
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
}

func TestDialAndAuthCmdTimedOut(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
		<-hold // never reply
	}))
	defer srv.Close()

	httpURL := "http://" + srv.Listener.Addr().String()
	msg := DialAndAuthCmd(DialAndAuthRequest{
		Server:      httpURL,
		Token:       validJWT(t, "u-rina"),
		Generation:  testGeneration,
		DialTimeout: time.Second,
		AuthTimeout: 200 * time.Millisecond,
	})()
	got, ok := msg.(WSAuthTimedOut)
	if !ok {
		t.Fatalf("expected WSAuthTimedOut, got %T", msg)
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
}

// captureSender records every tea.Msg sent into ReadLoopCmd. Used
// to assert frame dispatch + disconnect semantics without spinning
// up a tea.Program.
type captureSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
	done chan struct{}
}

func newCaptureSender() *captureSender {
	return &captureSender{done: make(chan struct{}, 1)}
}

func (c *captureSender) Send(m tea.Msg) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	if _, ok := m.(WSDisconnected); ok {
		select {
		case c.done <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
}

func (c *captureSender) snapshot() []tea.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]tea.Msg, len(c.msgs))
	copy(out, c.msgs)
	return out
}

func TestReadLoopCmdDispatchesFramesAndDisconnect(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]any{"type": "joined", "room_id": "r-1"})
		_ = conn.WriteJSON(map[string]any{"type": "message", "room_id": "r-1", "text": "hi"})
	}))
	defer srv.Close()

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(strings.Replace(srv.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	sender := newCaptureSender()
	if msg := ReadLoopCmd(ReadLoopRequest{
		Context:    context.Background(),
		Sender:     sender,
		Conn:       conn,
		Generation: testGeneration,
	})(); msg != nil {
		t.Fatalf("ReadLoopCmd should return nil immediately, got %T", msg)
	}

	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WSDisconnected")
	}

	msgs := sender.snapshot()
	if len(msgs) < 3 {
		t.Fatalf("expected at least 3 msgs (2 frames + disconnect), got %d: %#v", len(msgs), msgs)
	}
	// First two should be WSFrameReceived; last should be WSDisconnected.
	if fr, ok := msgs[0].(WSFrameReceived); !ok || fr.Type != "joined" {
		t.Errorf("msg[0] = %#v, want WSFrameReceived joined", msgs[0])
	} else if fr.Generation != testGeneration {
		t.Errorf("frame generation = %d, want %d", fr.Generation, testGeneration)
	}
	if fr, ok := msgs[1].(WSFrameReceived); !ok || fr.Type != "message" {
		t.Errorf("msg[1] = %#v, want WSFrameReceived message", msgs[1])
	} else if fr.Generation != testGeneration {
		t.Errorf("frame generation = %d, want %d", fr.Generation, testGeneration)
	}
	if dc, ok := msgs[len(msgs)-1].(WSDisconnected); !ok {
		t.Errorf("last msg = %#v, want WSDisconnected", msgs[len(msgs)-1])
	} else if dc.Generation != testGeneration {
		t.Errorf("disconnect generation = %d, want %d", dc.Generation, testGeneration)
	}
}

func TestReadLoopCmdAnswersPingWithPong(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	pongCh := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
			return
		}
		_, frame, err := conn.ReadMessage()
		if err != nil {
			return
		}
		pongCh <- frame
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	sender := newCaptureSender()
	if msg := ReadLoopCmd(ReadLoopRequest{
		Context:    context.Background(),
		Sender:     sender,
		Conn:       conn,
		Generation: testGeneration,
	})(); msg != nil {
		t.Fatalf("ReadLoopCmd should return nil immediately, got %T", msg)
	}

	select {
	case frame := <-pongCh:
		var env FrameEnvelope
		if err := json.Unmarshal(frame, &env); err != nil {
			t.Fatalf("decode pong reply: %v (raw %q)", err, frame)
		}
		if env.Type != "pong" {
			t.Fatalf("reply type = %q, want pong", env.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the pong reply")
	}

	// The server closes after receiving the pong; the loop reports the
	// disconnect as usual.
	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WSDisconnected")
	}

	// The ping is answered in place, never forwarded to the Update loop.
	for _, m := range sender.snapshot() {
		if fr, ok := m.(WSFrameReceived); ok {
			t.Errorf("frame forwarded to UI: %#v", fr)
		}
	}
}

func TestReadLoopCmdExitsWhenPongWriteFails(t *testing.T) {
	t.Parallel()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
			return
		}
		<-hold // keep the socket open so only the client's write path fails
	}))
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http", "ws", 1), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	// An already-expired write deadline makes the pong write fail while
	// reads keep working — the shape of a half-open connection.
	if err := conn.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("set write deadline: %v", err)
	}

	sender := newCaptureSender()
	if msg := ReadLoopCmd(ReadLoopRequest{
		Context:    context.Background(),
		Sender:     sender,
		Conn:       conn,
		Generation: testGeneration,
	})(); msg != nil {
		t.Fatalf("ReadLoopCmd should return nil immediately, got %T", msg)
	}

	// The failed pong write must end the loop with WSDisconnected rather
	// than leaving it blocked on a read that may never return.
	select {
	case <-sender.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WSDisconnected after pong-write failure")
	}
}

func TestLoginCmdTokenNeverLeaksToErrorPath(t *testing.T) {
	t.Parallel()
	const secretToken = "super.secret.jwt"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`malformed`))
	}))
	defer srv.Close()

	// Even if we encode a token-shaped password, the error path must
	// not return any message containing it.
	msg := LoginCmd(srv.Client(), srv.URL, "rina@example.com", secretToken, time.Second)()
	if rej, ok := msg.(LoginRejected); ok {
		if strings.Contains(rej.Message, secretToken) {
			t.Fatalf("LoginRejected message leaked password: %q", rej.Message)
		}
	}
	if te, ok := msg.(LoginTransportError); ok {
		if strings.Contains(te.Err.Error(), secretToken) {
			t.Fatalf("LoginTransportError leaked password: %q", te.Err)
		}
	}
}

// silence unused-import warning for errors when none of the
// specific-error helpers above are exercised by a given build.
var _ = errors.New
