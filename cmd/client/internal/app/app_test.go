package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// stubServer wires together an httptest.Server that hosts both
// POST /login and a WebSocket /ws endpoint, mimicking the real
// blabby gateway. behaviors are dialled in per test.
type stubServer struct {
	loginStatus int
	loginToken  string
	loginError  *api.ErrorEnvelope
	wsAccept    bool // true → respond auth_ok, false → respond auth_error
	wsError     *api.AuthErrorFrame

	srv      *httptest.Server
	upgrader websocket.Upgrader

	mu        sync.Mutex
	conn      *websocket.Conn
	authedCh  chan struct{}
	closedCh  chan struct{}
	gotPasswd string
}

func newStubServer(t *testing.T) *stubServer {
	t.Helper()
	s := &stubServer{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		authedCh: make(chan struct{}, 1),
		closedCh: make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/ws", s.handleWS)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.close)
	return s
}

func (s *stubServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "wrong method", http.StatusMethodNotAllowed)
		return
	}
	var req api.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.gotPasswd = req.Password
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if s.loginStatus == 0 {
		s.loginStatus = http.StatusOK
	}
	if s.loginStatus != http.StatusOK && s.loginError != nil {
		w.WriteHeader(s.loginStatus)
		_ = json.NewEncoder(w).Encode(s.loginError)
		return
	}
	w.WriteHeader(s.loginStatus)
	if s.loginStatus == http.StatusOK {
		_ = json.NewEncoder(w).Encode(api.LoginResponse{Token: s.loginToken})
	}
}

func (s *stubServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	defer func() {
		_ = conn.Close()
		select {
		case s.closedCh <- struct{}{}:
		default:
		}
	}()

	_, _, err = conn.ReadMessage()
	if err != nil {
		return
	}

	if s.wsAccept {
		_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
		select {
		case s.authedCh <- struct{}{}:
		default:
		}
		// Keep the connection open until the test closes it via
		// program teardown so the read loop has something to drain.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}

	if s.wsError == nil {
		s.wsError = &api.AuthErrorFrame{Type: "auth_error", Code: 1001, Status: "AUTH_INVALID_TOKEN", Message: "invalid token"}
	}
	_ = conn.WriteJSON(s.wsError)
}

func (s *stubServer) close() {
	s.srv.Close()
}

// validJWT mints a JWT-shaped string with the given sub claim.
// Signature segment is irrelevant — DecodeSub does not verify.
func validJWT(t *testing.T, sub string) string {
	t.Helper()
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := enc.EncodeToString([]byte(`{"sub":"` + sub + `"}`))
	sig := enc.EncodeToString([]byte("not-a-real-signature"))
	return header + "." + body + "." + sig
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// captureSender is the FrameSender used for the integration tests
// in place of a real *tea.Program. teatest does not expose the
// program pointer, so we accumulate frames into a buffer instead.
type captureSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (c *captureSender) Send(m tea.Msg) {
	c.mu.Lock()
	c.msgs = append(c.msgs, m)
	c.mu.Unlock()
}

func TestHappyPathOpensChromeWithProfilePopulated(t *testing.T) {
	stub := newStubServer(t)
	stub.loginStatus = http.StatusOK
	stub.loginToken = validJWT(t, "u-rina-1")
	stub.wsAccept = true

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()

	tm := teatest.NewTestModel(t, model,
		teatest.WithInitialTermSize(100, 30),
	)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Sign in to blabby")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	typeText(tm, "rina@example.com")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	typeText(tm, "hunter2")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "u-rina-1") && strings.Contains(s, "rina@example.com")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final, err := readAll(tm)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	finalStr := string(final)

	// Credentials hygiene: neither the typed password nor the JWT
	// must appear anywhere in the rendered output.
	if strings.Contains(finalStr, "hunter2") {
		t.Errorf("password leaked to rendered output")
	}
	if strings.Contains(finalStr, stub.loginToken) {
		t.Errorf("token leaked to rendered output")
	}
}

func TestLoginRejectedShowsInlineError(t *testing.T) {
	stub := newStubServer(t)
	stub.loginStatus = http.StatusUnauthorized
	stub.loginError = &api.ErrorEnvelope{Error: api.ErrorDetail{
		Code: 1001, Status: "AUTH_INVALID_TOKEN", Message: "invalid credentials",
	}}

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()

	tm := teatest.NewTestModel(t, model,
		teatest.WithInitialTermSize(100, 30),
	)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Sign in to blabby")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	typeText(tm, "rina@example.com")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	typeText(tm, "wrong")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Invalid credentials")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

func TestWSAuthErrorClosesAndRefocusesPassword(t *testing.T) {
	stub := newStubServer(t)
	stub.loginStatus = http.StatusOK
	stub.loginToken = validJWT(t, "u-rina-1")
	stub.wsAccept = false

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()

	tm := teatest.NewTestModel(t, model,
		teatest.WithInitialTermSize(100, 30),
	)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Sign in to blabby")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	typeText(tm, "rina@example.com")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	typeText(tm, "hunter2")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Invalid credentials")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	// After rejection: send a marker keystroke and verify that
	//   (a) password is the focused field (EchoPassword masks it),
	//   (b) the marker never leaks verbatim into any frame.
	// If the test regresses to focusing the email, the literal
	// 'Z' would render in the email row and this would catch it.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Z'}})
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final, err := readAll(tm)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.Contains(string(final), "Z") {
		t.Fatalf("marker 'Z' leaked into rendered output — focus was not on the password field")
	}
}

func typeText(tm *teatest.TestModel, s string) {
	for _, r := range s {
		tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// readAll drains the test model output into a buffer.
func readAll(tm *teatest.TestModel) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := tm.Output().Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if isEOF(err) {
				return buf, nil
			}
			return buf, err
		}
	}
}

// isEOF returns true if err signals end-of-stream for the test
// output. We avoid importing io here to keep the helper terse.
func isEOF(err error) bool {
	return err != nil && err.Error() == "EOF"
}
