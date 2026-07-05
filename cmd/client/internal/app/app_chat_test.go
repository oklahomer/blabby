package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

const echoTimestampMs int64 = 1_700_000_000_000

// chatStubServer hosts the endpoints the send/receive flow touches:
// /login, /ws (with server→client push), /rooms/joined (a single
// "general" room so the user has something to activate), and
// POST /rooms/{id}/messages. The /ws handler stores the connection so
// the test can push echo frames back to the client; every server→client
// write is serialised through writeMu to honour gorilla's single-writer
// contract.
type chatStubServer struct {
	loginToken string

	upgrader websocket.Upgrader
	srv      *httptest.Server

	connMu sync.Mutex
	conn   *websocket.Conn
	ready  chan struct{} // closed once auth_ok is sent and conn is stored

	writeMu sync.Mutex // serialises all server→client WS writes

	sendMu       sync.Mutex
	lastSendBody string
	lastSendAuth string
	sentCh       chan struct{} // signalled on each POST /messages
	sendStatus   int           // 0 → 200 success; http.StatusForbidden → ROOM_NOT_MEMBER
}

func newChatStubServer(t *testing.T) *chatStubServer {
	t.Helper()
	s := &chatStubServer{
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		ready:    make(chan struct{}),
		sentCh:   make(chan struct{}, 8),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/rooms/joined", s.handleRoomsJoined)
	mux.HandleFunc("/rooms/", s.handleRoomCommand)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *chatStubServer) handleLogin(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(api.LoginResponse{Token: s.loginToken})
}

func (s *chatStubServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	if _, _, err := conn.ReadMessage(); err != nil { // drain the auth frame
		return
	}
	s.writeMu.Lock()
	_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
	s.writeMu.Unlock()

	s.connMu.Lock()
	s.conn = conn
	s.connMu.Unlock()
	close(s.ready)

	// Keep reading so the client's close frame is observed and the
	// deferred Close runs; pushes happen from the test goroutine.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *chatStubServer) handleRoomsJoined(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(api.RoomListResponse{Rooms: []api.Room{{ID: "general", Name: "general"}}})
}

func (s *chatStubServer) handleRoomCommand(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/messages") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	s.sendMu.Lock()
	s.lastSendBody = string(raw)
	s.lastSendAuth = r.Header.Get("Authorization")
	s.sendMu.Unlock()
	select {
	case s.sentCh <- struct{}{}:
	default:
	}

	w.Header().Set("Content-Type", "application/json")
	if s.sendStatus == http.StatusForbidden {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: api.ErrorDetail{
			Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member",
		}})
		return
	}
	_ = json.NewEncoder(w).Encode(api.SendMessageResponse{Success: true, Timestamp: echoTimestampMs})
}

// pushMessage writes a server→client {"type":"message"} frame, blocking
// until the WebSocket is authenticated so the write never races auth_ok.
// eventID is carried as the frame's decimal event id so the client can
// order and dedup it.
func (s *chatStubServer) pushMessage(t *testing.T, room, sender, text string, eventID, ms int64) {
	t.Helper()
	select {
	case <-s.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket never became ready for push")
	}
	s.connMu.Lock()
	conn := s.conn
	s.connMu.Unlock()

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	frame := map[string]any{
		"type": "message", "room_id": room, "event_id": strconv.FormatInt(eventID, 10),
		"sender": map[string]any{"id": sender}, "text": text, "timestamp": ms,
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Errorf("push message frame: %v", err)
	}
}

// deferredSender forwards read-loop frames to a FrameSender installed
// after construction. teatest does not expose its program pointer, so
// the test wires the *teatest.TestModel in as the target once it exists,
// letting inbound WebSocket frames flow back into the model under test.
type deferredSender struct {
	mu     sync.Mutex
	target api.FrameSender
}

func (d *deferredSender) set(target api.FrameSender) {
	d.mu.Lock()
	d.target = target
	d.mu.Unlock()
}

func (d *deferredSender) Send(msg tea.Msg) {
	d.mu.Lock()
	target := d.target
	d.mu.Unlock()
	if target != nil {
		target.Send(msg)
	}
}

// driveToActiveRoomInput logs in, activates the single joined room, and
// moves focus to the composer so the caller can type and send.
func driveToActiveRoomInput(t *testing.T, tm *teatest.TestModel) {
	t.Helper()
	driveLogin(t, tm)
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "general")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	// Enter on the Rooms pane activates the room; the composer becomes
	// usable (its placeholder appears) once a room is active.
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "type a message")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	// Tab from Rooms → Main view → Main input.
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
}

func TestChatSendAndEchoRenders(t *testing.T) {
	stub := newChatStubServer(t)
	stub.loginToken = validJWT(t, "u-rina-1")

	sender := &deferredSender{}
	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(sender).
		OpenLoginModal()
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 35))
	sender.set(tm)

	driveToActiveRoomInput(t, tm)
	typeText(tm, "hello")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// The POST arrives with the bearer header and the typed text.
	select {
	case <-stub.sentCh:
	case <-time.After(5 * time.Second):
		t.Fatal("POST /messages never arrived")
	}
	stub.sendMu.Lock()
	gotAuth, gotBody := stub.lastSendAuth, stub.lastSendBody
	stub.sendMu.Unlock()
	if gotAuth != "Bearer "+stub.loginToken {
		t.Errorf("send auth header = %q, want bearer token", gotAuth)
	}
	if !strings.Contains(gotBody, `"hello"`) {
		t.Errorf("send body = %q, want it to carry text \"hello\"", gotBody)
	}

	// The server fans the echo back; it renders with a timestamp. The
	// rendered frame is captured for the credential-leak scan below.
	stub.pushMessage(t, "general", "u-rina-1", "hello", 1, echoTimestampMs)
	wantTS := time.UnixMilli(echoTimestampMs).Format("15:04:05")
	var rendered string
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		if strings.Contains(s, wantTS) && strings.Contains(s, "hello") {
			rendered = s
			return true
		}
		return false
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(5*time.Second))
	assertNoCredentialLeak(t, rendered, stub.loginToken)

	// Stop forwarding so the teardown WSDisconnected does not race the
	// quitting program, then shut down and inspect the final state.
	sender.set(nil)
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)

	if got := final.composer.Value(); got != "" {
		t.Errorf("composer not cleared after send: %q", got)
	}
	bucket := final.messages["general"]
	if len(bucket) != 1 || bucket[0].Text != "hello" {
		t.Fatalf("echoed message not in the scrollback bucket: %#v", bucket)
	}
	// The sender is the user themselves: the name is shown (here the raw id,
	// since this stub echo carries no display name) and the message is flagged
	// Self so mainview mutes it — it is no longer relabelled "you".
	if bucket[0].Sender != "u-rina-1" {
		t.Errorf("own echoed message sender = %q, want \"u-rina-1\"", bucket[0].Sender)
	}
	if !bucket[0].Self {
		t.Error("own echoed message should be flagged Self")
	}
}

func TestChatMessagesRenderInTimestampOrder(t *testing.T) {
	stub := newChatStubServer(t)
	stub.loginToken = validJWT(t, "u-rina-1")

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 35))

	driveLogin(t, tm)
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "general")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // activate "general"
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "type a message")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	// Inject two frames out of order: the later timestamp arrives first.
	tm.Send(api.WSFrameReceived{Type: "message", Raw: messageFrameJSON("general", "bob", "bravo-msg", 5, 5000), Generation: 1})
	tm.Send(api.WSFrameReceived{Type: "message", Raw: messageFrameJSON("general", "alice", "alpha-msg", 1, 1000), Generation: 1})

	// Both must reach the rendered scrollback (the render path runs).
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "alpha-msg") && strings.Contains(s, "bravo-msg")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)

	bucket := final.messages["general"]
	if len(bucket) != 2 {
		t.Fatalf("expected 2 messages in the bucket, got %d: %#v", len(bucket), bucket)
	}
	// Ordered by server timestamp (1000 before 5000), not arrival order.
	if bucket[0].Text != "alpha-msg" || bucket[1].Text != "bravo-msg" {
		t.Errorf("messages not ordered by timestamp: %#v", bucket)
	}
}

func TestChatSendFailureKeepsSession(t *testing.T) {
	stub := newChatStubServer(t)
	stub.loginToken = validJWT(t, "u-rina-1")
	stub.sendStatus = http.StatusForbidden

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()
	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 35))

	driveToActiveRoomInput(t, tm)
	typeText(tm, "hello")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	// The humanised business error renders inline in the Main pane.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Not a member of this room")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	final := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)

	// The session stays intact: the connection is not torn down and no
	// login modal is reopened — only the inline error is shown.
	if final.conn == nil {
		t.Error("send failure tore down the WebSocket connection")
	}
	if final.modal != nil {
		t.Errorf("send failure reopened a modal: %T", final.modal)
	}
	if final.mainError != "Not a member of this room" {
		t.Errorf("mainError = %q, want humanised business error", final.mainError)
	}
}

// assertNoCredentialLeak fails the test if the JWT or the password typed
// during login appears anywhere in the supplied rendered output —
// credentials must never reach the screen.
func assertNoCredentialLeak(t *testing.T, rendered, token string) {
	t.Helper()
	if strings.Contains(rendered, token) {
		t.Error("token leaked to rendered output")
	}
	if strings.Contains(rendered, "hunter2") {
		t.Error("password leaked to rendered output")
	}
}
