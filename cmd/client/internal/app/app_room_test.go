package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// roomStubServer mounts the four endpoints the room-browsing flow
// touches: /login, /ws, /rooms, /rooms/joined, POST /rooms/{id}/join.
// behavior is set through joinPolicy / joinedSequence so tests can
// dial in 200 / 409 / 503 outcomes without rewriting the handler
// fixtures every time.
type roomStubServer struct {
	loginToken string
	rooms      []api.Room
	// joinedSequence supplies a different /rooms/joined response on
	// each call (clamps to the last entry once exhausted). Lets the
	// happy-path test simulate "empty before join, populated after".
	joinedSequence [][]string
	// joinPolicy decides the join response per room ID. Defaults to
	// 200 success for unmapped rooms. Set entries to 409, 404, 503
	// to surface the matching server outcomes.
	joinPolicy map[string]int
	// roomsStatus, when non-zero, overrides the /rooms response with
	// that HTTP status + a SERVICE_UNAVAILABLE envelope. Used by the
	// search-load-failed scenario.
	roomsStatus int

	srv      *httptest.Server
	upgrader websocket.Upgrader

	joinedCalls int32
}

func newRoomStubServer(t *testing.T) *roomStubServer {
	t.Helper()
	s := &roomStubServer{
		upgrader:   websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		joinPolicy: map[string]int{},
		rooms: []api.Room{
			{ID: "general", Name: "General"},
			{ID: "random", Name: "Random"},
		},
		joinedSequence: [][]string{{}, {"general"}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/rooms", s.handleRoomList)
	mux.HandleFunc("/rooms/joined", s.handleRoomsJoined)
	mux.HandleFunc("/rooms/", s.handleRoomCommand)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *roomStubServer) handleLogin(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(api.LoginResponse{Token: s.loginToken})
}

func (s *roomStubServer) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	if _, _, err := conn.ReadMessage(); err != nil {
		return
	}
	_ = conn.WriteJSON(map[string]string{"type": "auth_ok"})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *roomStubServer) handleRoomList(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.roomsStatus != 0 {
		w.WriteHeader(s.roomsStatus)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: api.ErrorDetail{
			Code: 5002, Status: "SERVICE_UNAVAILABLE", Message: "down",
		}})
		return
	}
	_ = json.NewEncoder(w).Encode(api.RoomListResponse{Rooms: s.rooms})
}

func (s *roomStubServer) handleRoomsJoined(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	idx := atomic.LoadInt32(&s.joinedCalls)
	atomic.AddInt32(&s.joinedCalls, 1)
	seq := s.joinedSequence
	if len(seq) == 0 {
		_ = json.NewEncoder(w).Encode(api.JoinedRoomsResponse{RoomIDs: []string{}})
		return
	}
	if int(idx) >= len(seq) {
		idx = int32(len(seq) - 1)
	}
	roomIDs := seq[idx]
	if roomIDs == nil {
		roomIDs = []string{}
	}
	_ = json.NewEncoder(w).Encode(api.JoinedRoomsResponse{RoomIDs: roomIDs})
}

func (s *roomStubServer) handleRoomCommand(w http.ResponseWriter, r *http.Request) {
	// Expect /rooms/{id}/join — strip the prefix/suffix.
	if !strings.HasPrefix(r.URL.Path, "/rooms/") || !strings.HasSuffix(r.URL.Path, "/join") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	roomID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/rooms/"), "/join")
	status, ok := s.joinPolicy[roomID]
	if !ok {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	switch status {
	case http.StatusOK:
		_ = json.NewEncoder(w).Encode(api.JoinSuccessResponse{Success: true})
	case http.StatusConflict:
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: api.ErrorDetail{
			Code: 2002, Status: "ROOM_ALREADY_MEMBER", Message: "already joined",
		}})
	case http.StatusNotFound:
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: api.ErrorDetail{
			Code: 2003, Status: "ROOM_NOT_FOUND", Message: "no such room",
		}})
	default:
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(api.ErrorEnvelope{Error: api.ErrorDetail{
			Code: 5002, Status: "SERVICE_UNAVAILABLE", Message: "down",
		}})
	}
}

func driveLogin(t *testing.T, tm *teatest.TestModel) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Sign in to blabby")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))
	typeText(tm, "rina")
	tm.Send(tea.KeyMsg{Type: tea.KeyTab})
	typeText(tm, "hunter2")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestRoomSearchHappyPathJoinFlow(t *testing.T) {
	stub := newRoomStubServer(t)
	stub.loginToken = validJWT(t, "u-rina-1")

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 35))
	driveLogin(t, tm)

	// After auth, the joined list is empty so the Rooms pane shows
	// the "no rooms yet" placeholder.
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "(no rooms yet)")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	// Open search modal with '/'. The modal opens in phaseLoading and
	// fills with the catalogue once /rooms responds; a single WaitFor
	// asserts both the modal opening and the catalogue arriving,
	// because bubbletea's diff-renderer emits each row only once and
	// a follow-up WaitFor would not see the same substrings again.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "Search rooms") && strings.Contains(s, "General")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(5*time.Second))

	// Cursor down to Random, up back to General, then enter joins.
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyUp})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		// After the modal closes, the Rooms pane renders "› General"
		// (the cursor row, resolved from the in-session name cache)
		// and the Main pane title also reads "General". Wait for both.
		return strings.Contains(s, "› General") && strings.Contains(s, "rina")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final, err := readAll(tm)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	finalStr := string(final)
	if strings.Contains(finalStr, stub.loginToken) {
		t.Errorf("token leaked to rendered output")
	}
	if strings.Contains(finalStr, "hunter2") {
		t.Errorf("password leaked to rendered output")
	}
}

func TestRoomJoinAlreadyMemberKeepsModalOpen(t *testing.T) {
	stub := newRoomStubServer(t)
	stub.loginToken = validJWT(t, "u-rina-1")
	stub.joinPolicy = map[string]int{"random": http.StatusConflict}

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 35))
	driveLogin(t, tm)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "(no rooms yet)")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Random")
	}, teatest.WithCheckInterval(50*time.Millisecond), teatest.WithDuration(3*time.Second))

	// Cursor down to Random and enter.
	tm.Send(tea.KeyMsg{Type: tea.KeyDown})
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		s := string(out)
		return strings.Contains(s, "Already joined this room") && strings.Contains(s, "Search rooms")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))

	final, err := readAll(tm)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.Contains(string(final), stub.loginToken) {
		t.Errorf("token leaked to rendered output")
	}
}

func TestRoomSearchLoadFailureShowsMappedError(t *testing.T) {
	stub := newRoomStubServer(t)
	stub.loginToken = validJWT(t, "u-rina-1")
	stub.roomsStatus = http.StatusServiceUnavailable

	model := New(mustURL(t, stub.srv.URL), stub.srv.Client()).
		SetProgram(&captureSender{}).
		OpenLoginModal()

	tm := teatest.NewTestModel(t, model, teatest.WithInitialTermSize(120, 35))
	driveLogin(t, tm)

	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "(no rooms yet)")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return strings.Contains(string(out), "Server unavailable")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(5*time.Second))

	// Dismiss the modal with esc.
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	teatest.WaitFor(t, tm.Output(), func(out []byte) bool {
		return !strings.Contains(string(out), "Search rooms")
	}, teatest.WithCheckInterval(100*time.Millisecond), teatest.WithDuration(3*time.Second))

	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}
