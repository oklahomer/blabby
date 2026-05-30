package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
)

// makeModel builds a baseline app.Model with the login modal
// installed and a no-op FrameSender wired in. Dispatch-table tests
// can mutate fields (modal, focus, conn, ...) before driving Update.
func makeModel(t *testing.T) Model {
	t.Helper()
	u, _ := url.Parse("http://localhost:8080")
	return New(u, &http.Client{}).SetProgram(&captureSender{}).OpenLoginModal()
}

func TestUpdateWindowSizeMsg(t *testing.T) {
	m := makeModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got := next.(Model)
	if got.width != 120 || got.height != 40 {
		t.Fatalf("got %dx%d, want 120x40", got.width, got.height)
	}
}

func TestUpdateTickAdvancesClockAndReissues(t *testing.T) {
	m := makeModel(t)
	now := time.Date(2026, 5, 24, 14, 22, 1, 0, time.Local)
	next, cmd := m.Update(tickMsg(now))
	got := next.(Model)
	if !got.now.Equal(now) {
		t.Fatalf("clock did not advance; got %v want %v", got.now, now)
	}
	if !got.infoState.Now.Equal(now) {
		t.Fatalf("infoState.Now not synced; got %v", got.infoState.Now)
	}
	if cmd == nil {
		t.Fatal("expected tickEverySecond cmd to be re-issued")
	}
}

func TestUpdateCtrlCQuitsAndClosesConn(t *testing.T) {
	m := makeModel(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd")
	}
}

func TestUpdateFocusInterpretedOnlyWhenNoModal(t *testing.T) {
	m := makeModel(t)
	m.modal = nil // close the modal so focus keys reach interpret
	m.focus = focusRooms

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := next.(Model)
	if got.focus != focusMainView {
		t.Fatalf("focus = %v, want focusMainView", got.focus)
	}
}

func TestUpdateLoginSucceededAdvancesToConnecting(t *testing.T) {
	m := makeModel(t)
	next, cmd := m.Update(api.LoginSucceeded{Token: "fake.jwt.tok", Username: "rina"})
	if cmd == nil {
		t.Fatal("expected DialAndAuthCmd to fire")
	}
	got := next.(Model)
	if got.token != "fake.jwt.tok" {
		t.Fatalf("token not retained: %q", got.token)
	}
	if got.username != "rina" {
		t.Fatalf("username not retained: %q", got.username)
	}
}

func TestUpdateLoginRejectedForwardedToModal(t *testing.T) {
	m := makeModel(t)
	m.width, m.height = 100, 30
	rej := api.LoginRejected{Status: "AUTH_INVALID_TOKEN", Message: "bad creds"}
	next, _ := m.Update(rej)
	got := next.(Model)
	view := got.View()
	if !strings.Contains(view, "Invalid credentials") {
		t.Fatalf("modal did not render error; view:\n%s", view)
	}
}

func TestUpdateWSAuthRejectedSurfacedByModal(t *testing.T) {
	m := makeModel(t)
	next, _ := m.Update(api.WSAuthRejected{Status: "AUTH_INVALID_TOKEN", Message: "rejected"})
	got := next.(Model)
	if got.modal == nil {
		t.Fatal("expected modal to remain open")
	}
}

func TestUpdateWSDisconnectedReopensLoginModal(t *testing.T) {
	m := makeModel(t)
	m.width, m.height = 100, 30
	m.modal = nil
	m.token = "fake.jwt"
	m.username = "rina"
	m.userID = "u-rina-1"
	m.infoState.Username = "rina"
	m.infoState.UserID = "u-rina-1"

	next, cmd := m.Update(api.WSDisconnected{Err: errors.New("server closed")})
	got := next.(Model)

	if got.modal == nil {
		t.Fatal("expected login modal re-opened")
	}
	if got.token != "" || got.username != "" || got.userID != "" {
		t.Fatal("expected session cleared")
	}
	if got.infoState.Username != "" || got.infoState.UserID != "" {
		t.Fatal("expected Profile cleared")
	}
	if cmd == nil {
		t.Fatal("expected modal Init cmd")
	}

	view := got.View()
	if !strings.Contains(view, "Connection lost") {
		t.Fatalf("expected connection-lost headline; view:\n%s", view)
	}
}

func TestUpdateFrameReceivedDroppedSilently(t *testing.T) {
	m := makeModel(t)
	next, cmd := m.Update(api.WSFrameReceived{Type: "message", Raw: []byte(`{"type":"message"}`)})
	if cmd != nil {
		t.Fatal("inbound frames must not produce a cmd from the base dispatch")
	}
	if _, ok := next.(Model); !ok {
		t.Fatal("expected Model returned")
	}
}

func TestViewBeforeWindowSizeReturnsEmpty(t *testing.T) {
	m := makeModel(t)
	if m.View() != "" {
		t.Fatal("expected empty view before WindowSizeMsg")
	}
}

func TestAdvanceLoginToConnectingNoOpWhenWrongModal(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	got, _ := m.advanceLoginToConnecting()
	if got != nil {
		t.Fatalf("expected nil modal, got %T", got)
	}
}

// captureSender lives in app_test.go; this file shares it via the
// package boundary. The interface dependency on api.FrameSender means
// any FrameSender satisfies SetProgram(), including the no-op one
// used by tests that never exercise the WS read loop.
var _ api.FrameSender = (*captureSender)(nil)

func TestUpdateRoomJoinedSetsActiveRoomAndCloses(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	// RoomJoined requires a live session — conn must be non-nil so the
	// post-WSDisconnected race guard does not drop the message.
	m.conn = &websocket.Conn{}

	next, cmd := m.Update(api.RoomJoined{RoomID: "general", RoomName: "General"})
	got := next.(Model)

	if got.activeRoomID != "general" {
		t.Fatalf("activeRoomID = %q, want general", got.activeRoomID)
	}
	if got.mainviewState.RoomLabel != "General" {
		t.Fatalf("RoomLabel = %q, want General", got.mainviewState.RoomLabel)
	}
	if got.nameForID["general"] != "General" {
		t.Fatalf("nameForID not populated: %#v", got.nameForID)
	}
	if got.modal != nil {
		t.Fatal("expected modal cleared on RoomJoined")
	}
	if cmd == nil {
		t.Fatal("expected JoinedRooms reload Cmd")
	}
}

func TestUpdateRoomJoinedAfterSessionEndsIsDropped(t *testing.T) {
	// Join HTTP completes after WSDisconnected wiped the session. The
	// guard must drop the message so the freshly-opened login modal
	// stays put and no phantom active-room state is written.
	m := makeModel(t) // login modal installed; token == ""; conn == nil

	next, cmd := m.Update(api.RoomJoined{RoomID: "general", RoomName: "General"})
	got := next.(Model)

	if got.activeRoomID != "" {
		t.Fatalf("activeRoomID set to %q on dead session", got.activeRoomID)
	}
	if got.mainviewState.RoomLabel != "" {
		t.Fatalf("RoomLabel set to %q on dead session", got.mainviewState.RoomLabel)
	}
	if got.nameForID != nil {
		t.Fatalf("nameForID populated on dead session: %#v", got.nameForID)
	}
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("login modal cleared by stale RoomJoined; got %T", got.modal)
	}
	if cmd != nil {
		t.Fatal("expected no Cmd dispatched on dead session")
	}
}

func TestUpdateRoomsLoadFailedUnauthorizedTriggersSessionExpiry(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.username = "rina"

	next, cmd := m.Update(api.RoomsLoadFailed{HTTPStatus: http.StatusUnauthorized})
	got := next.(Model)

	if got.token != "" {
		t.Fatalf("token not cleared: %q", got.token)
	}
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("expected login.Model installed on session expiry, got %T", got.modal)
	}
	if cmd == nil {
		t.Fatal("expected login modal Init Cmd")
	}
}

func TestUpdateRoomJoinFailedUnauthorizedTriggersSessionExpiry(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.username = "rina"

	next, cmd := m.Update(api.RoomJoinFailed{HTTPStatus: http.StatusUnauthorized})
	got := next.(Model)

	if got.token != "" {
		t.Fatalf("token not cleared: %q", got.token)
	}
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("expected login.Model installed on session expiry, got %T", got.modal)
	}
	if cmd == nil {
		t.Fatal("expected login modal Init Cmd")
	}
}

func TestUpdateSessionExpiryClearsNameForID(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.nameForID = map[string]string{"general": "General"}

	next, _ := m.Update(api.JoinedRoomsLoadFailed{HTTPStatus: http.StatusUnauthorized})
	got := next.(Model)

	if got.nameForID != nil {
		t.Fatalf("nameForID not cleared on session expiry: %#v", got.nameForID)
	}
}

func TestUpdateJoinedRoomsLoadedPopulatesPane(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.roomsState.Loading = true

	next, _ := m.Update(api.JoinedRoomsLoaded{RoomIDs: []string{"general", "random"}})
	got := next.(Model)
	if got.roomsState.Loading {
		t.Fatal("Loading flag not cleared")
	}
	if len(got.roomsState.JoinedIDs) != 2 {
		t.Fatalf("JoinedIDs not stored: %#v", got.roomsState.JoinedIDs)
	}
}

func TestUpdateJoinedRoomsLoadFailedShowsError(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.roomsState.Loading = true

	next, _ := m.Update(api.JoinedRoomsLoadFailed{
		Status: "SERVICE_UNAVAILABLE", Message: "down", HTTPStatus: 503,
	})
	got := next.(Model)
	if got.roomsState.LoadError == "" {
		t.Fatal("LoadError not set")
	}
	if got.roomsState.Loading {
		t.Fatal("Loading flag not cleared")
	}
}

func TestUpdateJoinedRoomsLoadFailedUnauthorizedReopensLogin(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.width, m.height = 100, 30
	m.token = "fake.jwt"
	m.username = "rina"
	m.userID = "u-rina-1"

	next, cmd := m.Update(api.JoinedRoomsLoadFailed{HTTPStatus: 401, Status: "AUTH_EXPIRED_TOKEN"})
	got := next.(Model)
	if got.token != "" {
		t.Fatal("token not discarded on 401")
	}
	if got.modal == nil {
		t.Fatal("expected login modal reopened")
	}
	if cmd == nil {
		t.Fatal("expected modal Init cmd")
	}
	view := got.View()
	if !strings.Contains(view, "Session expired") {
		t.Fatalf("expected session-expired headline; view:\n%s", view)
	}
}

func TestHandleKeySlashOpensSearchModalPostAuth(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	got := next.(Model)
	if got.modal == nil {
		t.Fatal("expected search modal opened")
	}
	if cmd == nil {
		t.Fatal("expected Init Cmd from search modal")
	}
}

func TestHandleKeySlashIgnoredPreAuth(t *testing.T) {
	m := makeModel(t)
	if _, ok := m.modal.(login.Model); !ok {
		t.Fatalf("test setup: expected login.Model installed pre-auth, got %T", m.modal)
	}
	// Login modal still installed; key flows to the modal — the
	// search modal must NOT open here.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	got := next.(Model)
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("expected login.Model to remain, got %T", got.modal)
	}
}

func TestHandleKeyRoomsPaneRetryDispatchesLoad(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.focus = focusRooms
	m.roomsState.LoadError = "boom"

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("expected retry Cmd")
	}
}

func TestHandleKeyRoomsPaneEnterSwitchesActiveRoom(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.focus = focusRooms
	m.roomsState.JoinedIDs = []string{"general", "random"}
	m.roomsState.NameForID = map[string]string{"general": "General"}
	m.roomsState.Cursor = 0

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if got.activeRoomID != "general" {
		t.Fatalf("activeRoomID = %q, want general", got.activeRoomID)
	}
	if got.mainviewState.RoomLabel != "General" {
		t.Fatalf("RoomLabel = %q, want General", got.mainviewState.RoomLabel)
	}
}
