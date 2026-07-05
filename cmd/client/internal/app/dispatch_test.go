package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
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
	next, cmd := m.Update(api.LoginSucceeded{Token: "fake.jwt.tok", Email: "rina@example.com"})
	if cmd == nil {
		t.Fatal("expected DialAndAuthCmd to fire")
	}
	got := next.(Model)
	if got.token != "fake.jwt.tok" {
		t.Fatalf("token not retained: %q", got.token)
	}
	if got.email != "rina@example.com" {
		t.Fatalf("email not retained: %q", got.email)
	}
	if got.sessionGeneration != 1 {
		t.Fatalf("sessionGeneration = %d, want 1", got.sessionGeneration)
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
	m.email = "rina@example.com"
	m.userID = "u-rina-1"
	m.infoState.Email = "rina@example.com"
	m.infoState.UserID = "u-rina-1"
	m.connected = true
	m.messages = map[string][]mainview.Message{"general": {{Sender: "alice", Text: "hi"}}}
	m.mainError = "stale error"

	next, cmd := m.Update(api.WSDisconnected{Err: errors.New("server closed")})
	got := next.(Model)

	if got.modal == nil {
		t.Fatal("expected login modal re-opened")
	}
	if got.token != "" || got.email != "" || got.userID != "" {
		t.Fatal("expected session cleared")
	}
	if got.infoState.Email != "" || got.infoState.UserID != "" {
		t.Fatal("expected Profile cleared")
	}
	// The passive status indicator and the chat surface must reset on a
	// drop so the reopened session does not show ● live or a stale
	// scrollback / error.
	if got.connected {
		t.Fatal("expected connected=false after disconnect")
	}
	if got.messages != nil {
		t.Fatalf("expected message buckets cleared, got %#v", got.messages)
	}
	if got.mainError != "" {
		t.Fatalf("expected inline error cleared, got %q", got.mainError)
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
	m.sessionGeneration = 1
	// RoomJoined requires a live session — conn must be non-nil so the
	// post-WSDisconnected race guard does not drop the message.
	m.conn = &websocket.Conn{}

	next, cmd := m.Update(api.RoomJoined{RoomID: "general", RoomName: "General", Generation: m.sessionGeneration})
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

	next, cmd := m.Update(api.RoomJoined{RoomID: "general", RoomName: "General", Generation: 1})
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
	m.email = "rina@example.com"
	m.sessionGeneration = 1

	next, cmd := m.Update(api.RoomsLoadFailed{HTTPStatus: http.StatusUnauthorized, Generation: m.sessionGeneration})
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
	m.email = "rina@example.com"
	m.sessionGeneration = 1

	next, cmd := m.Update(api.RoomJoinFailed{HTTPStatus: http.StatusUnauthorized, Generation: m.sessionGeneration})
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

func TestUpdateRoomsLoadFailedUnauthorizedFromOldGenerationDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.sessionGeneration = 2

	next, cmd := m.Update(api.RoomsLoadFailed{
		HTTPStatus: http.StatusUnauthorized,
		Status:     "AUTH_EXPIRED_TOKEN",
		Generation: 1,
	})
	got := next.(Model)

	if cmd != nil {
		t.Fatal("stale room-list failure must not dispatch a command")
	}
	if got.token == "" || got.conn == nil {
		t.Fatal("stale room-list failure expired the current session")
	}
	if got.modal != nil {
		t.Fatalf("stale room-list failure opened a modal: %T", got.modal)
	}
}

func TestUpdateSessionExpiryClearsNameForID(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.nameForID = map[string]string{"general": "General"}
	m.sessionGeneration = 1

	next, _ := m.Update(api.JoinedRoomsLoadFailed{HTTPStatus: http.StatusUnauthorized, Generation: m.sessionGeneration})
	got := next.(Model)

	if got.nameForID != nil {
		t.Fatalf("nameForID not cleared on session expiry: %#v", got.nameForID)
	}
}

func TestUpdateJoinedRoomsLoadedPopulatesPane(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.roomsState.Loading = true
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.sessionGeneration = 1

	next, _ := m.Update(api.JoinedRoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
		{ID: "random", Name: "Random"},
	}, Generation: m.sessionGeneration})
	got := next.(Model)
	if got.roomsState.Loading {
		t.Fatal("Loading flag not cleared")
	}
	if len(got.roomsState.JoinedIDs) != 2 {
		t.Fatalf("JoinedIDs not stored: %#v", got.roomsState.JoinedIDs)
	}
	if got.roomsState.NameForID["general"] != "General" {
		t.Fatalf("descriptor name not populated into NameForID: %#v", got.roomsState.NameForID)
	}
}

func TestUpdateJoinedRoomsLoadFailedShowsError(t *testing.T) {
	m := makeModel(t)
	m.modal = nil
	m.roomsState.Loading = true
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.sessionGeneration = 1

	next, _ := m.Update(api.JoinedRoomsLoadFailed{
		Status: "SERVICE_UNAVAILABLE", Message: "down", HTTPStatus: 503, Generation: m.sessionGeneration,
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
	m.email = "rina@example.com"
	m.userID = "u-rina-1"
	m.conn = &websocket.Conn{}
	m.sessionGeneration = 1

	next, cmd := m.Update(api.JoinedRoomsLoadFailed{
		HTTPStatus: 401, Status: "AUTH_EXPIRED_TOKEN", Generation: m.sessionGeneration,
	})
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

// chatReadyModel returns a Model in a post-auth, room-active state with
// the chat surface usable: a live connection, an initialised bucket
// map, and focus on the input region.
func chatReadyModel(t *testing.T) Model {
	t.Helper()
	m := makeModel(t)
	m.modal = nil
	m.token = "fake.jwt"
	m.conn = &websocket.Conn{}
	m.userID = "u-rina-1"
	m.connected = true
	m.sessionGeneration = 1
	m.messages = map[string][]mainview.Message{}
	m.activeRoomID = "general"
	m.mainviewState.RoomLabel = "general"
	m.focus = focusMainInput
	return m
}

// messageFrameJSON builds a raw {"type":"message"} frame for dispatch
// tests without threading json.Marshal through every call site. eventID
// is the ordering key the scrollback sorts and dedups on.
func messageFrameJSON(room, sender, text string, eventID, ms int64) []byte {
	return []byte(fmt.Sprintf(
		`{"type":"message","room_id":%q,"event_id":%q,"sender":{"id":%q},"text":%q,"timestamp":%d}`,
		room, itoa64(eventID), sender, text, ms))
}

func messageFrameJSONNamed(room, senderID, senderName, text string, eventID, ms int64) []byte {
	return []byte(fmt.Sprintf(
		`{"type":"message","room_id":%q,"event_id":%q,"sender":{"id":%q,"name":%q},"text":%q,"timestamp":%d}`,
		room, itoa64(eventID), senderID, senderName, text, ms))
}

// memberFrameJSON builds a raw {"type":"joined"|"left"} membership frame.
func memberFrameJSON(kind, room, userID, userName string, eventID, ms int64) []byte {
	return []byte(fmt.Sprintf(
		`{"type":%q,"room_id":%q,"event_id":%q,"user":{"id":%q,"name":%q},"timestamp":%d}`,
		kind, room, itoa64(eventID), userID, userName, ms))
}

// itoa64 renders a decimal event id for the wire fixtures.
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

func chatFrame(m Model, typ string, raw []byte) api.WSFrameReceived {
	return api.WSFrameReceived{Type: typ, Raw: raw, Generation: m.sessionGeneration}
}

func TestUpdateMessageFrameAppendsToActiveBucket(t *testing.T) {
	m := chatReadyModel(t)
	next, cmd := m.Update(chatFrame(m, "message", messageFrameJSON("general", "alice", "hello", 1, 1000)))
	got := next.(Model)
	if cmd != nil {
		t.Fatal("inbound message frame must not dispatch a cmd")
	}
	bucket := got.messages["general"]
	if len(bucket) != 1 || bucket[0].Text != "hello" || bucket[0].Sender != "alice" {
		t.Fatalf("bucket = %#v", bucket)
	}
}

func TestUpdateMessageFrameSortsByEventID(t *testing.T) {
	m := chatReadyModel(t)
	// event_id order and timestamp order disagree: "first" has the smaller
	// event id but the later timestamp. Ordering by event id must win.
	n1, _ := m.Update(chatFrame(m, "message", messageFrameJSON("general", "a", "second", 20, 1000)))
	n1Model := n1.(Model)
	n2, _ := n1Model.Update(chatFrame(n1Model, "message", messageFrameJSON("general", "b", "first", 10, 9000)))
	bucket := n2.(Model).messages["general"]
	if len(bucket) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(bucket))
	}
	if bucket[0].Text != "first" || bucket[1].Text != "second" {
		t.Fatalf("messages not sorted by event id: %#v", bucket)
	}
}

func TestUpdateMessageFrameDedupsByEventID(t *testing.T) {
	m := chatReadyModel(t)
	// The same event id arriving twice (e.g. a live frame that also lands
	// via backfill) is inserted once.
	n1, _ := m.Update(chatFrame(m, "message", messageFrameJSON("general", "a", "hello", 42, 1000)))
	n1Model := n1.(Model)
	n2, _ := n1Model.Update(chatFrame(n1Model, "message", messageFrameJSON("general", "a", "hello", 42, 1000)))
	bucket := n2.(Model).messages["general"]
	if len(bucket) != 1 {
		t.Fatalf("expected 1 deduped message, got %d: %#v", len(bucket), bucket)
	}
}

func TestUpdateMemberFramesAppendSystemLines(t *testing.T) {
	m := chatReadyModel(t)
	n1, cmd := m.Update(chatFrame(m, "joined", memberFrameJSON("joined", "general", "U9", "Bob", 5, 1000)))
	if cmd != nil {
		t.Fatal("a membership frame must not dispatch a cmd")
	}
	n2, _ := n1.(Model).Update(chatFrame(n1.(Model), "left", memberFrameJSON("left", "general", "U9", "Bob", 6, 2000)))
	bucket := n2.(Model).messages["general"]
	if len(bucket) != 2 {
		t.Fatalf("expected 2 system lines, got %d: %#v", len(bucket), bucket)
	}
	if bucket[0].Kind != mainview.KindJoined || bucket[0].Sender != "Bob" {
		t.Fatalf("first entry not a joined system line: %#v", bucket[0])
	}
	if bucket[1].Kind != mainview.KindLeft {
		t.Fatalf("second entry not a left system line: %#v", bucket[1])
	}
}

func TestUpdateMemberFrameMalformedIgnored(t *testing.T) {
	m := chatReadyModel(t)
	next, _ := m.Update(chatFrame(m, "joined", []byte(`{"type":"joined"}`))) // no event_id
	if len(next.(Model).messages["general"]) != 0 {
		t.Fatal("a membership frame without an event id must not append")
	}
}

func TestUpdateMessageFrameForOtherRoomRetainedNotShown(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 120, 35
	next, _ := m.Update(chatFrame(m, "message", messageFrameJSON("random", "a", "hidden-text", 1, 1000)))
	got := next.(Model)
	if len(got.messages["random"]) != 1 {
		t.Fatalf("frame for non-active room not retained: %#v", got.messages)
	}
	if len(got.messages["general"]) != 0 {
		t.Fatalf("frame leaked into the active room: %#v", got.messages["general"])
	}
	if strings.Contains(got.View(), "hidden-text") {
		t.Fatal("non-active room message rendered in the active scrollback")
	}
}

func TestUpdateOwnMessageShowsMutedName(t *testing.T) {
	m := chatReadyModel(t) // userID == u-rina-1
	next, _ := m.Update(chatFrame(m, "message", messageFrameJSONNamed("general", "u-rina-1", "Rina", "mine", 1, 1000)))
	msg := next.(Model).messages["general"][0]
	// Own messages now show the display name (not "you") and are flagged
	// Self so mainview mutes the sender.
	if msg.Sender != "Rina" {
		t.Errorf("own message sender = %q, want display name %q", msg.Sender, "Rina")
	}
	if !msg.Self {
		t.Error("own message should be flagged Self")
	}
}

func TestUpdateOtherUserMessageNotSelf(t *testing.T) {
	m := chatReadyModel(t) // userID == u-rina-1
	next, _ := m.Update(chatFrame(m, "message", messageFrameJSONNamed("general", "u-bob-9", "Bob", "hi", 1, 1000)))
	msg := next.(Model).messages["general"][0]
	if msg.Sender != "Bob" {
		t.Errorf("other sender = %q, want %q", msg.Sender, "Bob")
	}
	if msg.Self {
		t.Error("another user's message must not be flagged Self")
	}
}

func TestUpdateMessageFrameMalformedIgnored(t *testing.T) {
	m := chatReadyModel(t)
	next, _ := m.Update(chatFrame(m, "message", []byte(`{bad json`)))
	if len(next.(Model).messages["general"]) != 0 {
		t.Fatal("malformed message frame must not append")
	}
}

func TestUpdateErrorFrameSetsInlineError(t *testing.T) {
	m := chatReadyModel(t)
	raw := []byte(`{"type":"error","code":2001,"status":"ROOM_NOT_MEMBER","message":"x"}`)
	next, _ := m.Update(chatFrame(m, "error", raw))
	if got := next.(Model).mainError; got != "Not a member of this room" {
		t.Fatalf("mainError = %q", got)
	}
}

func TestUpdateSendMessageFailedUnauthorizedReopensLogin(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.messages["general"] = []mainview.Message{{Sender: "alice", Text: "hi"}}
	m.mainError = "stale error"
	next, cmd := m.Update(api.SendMessageFailed{
		Generation: m.sessionGeneration,
		HTTPStatus: http.StatusUnauthorized,
		Status:     "AUTH_EXPIRED_TOKEN",
	})
	got := next.(Model)
	if got.token != "" {
		t.Fatalf("token not discarded on 401: %q", got.token)
	}
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("expected login modal reopened, got %T", got.modal)
	}
	// Session expiry tears down the chat surface just like a disconnect.
	if got.connected {
		t.Fatal("expected connected=false after session expiry")
	}
	if got.messages != nil {
		t.Fatalf("expected message buckets cleared, got %#v", got.messages)
	}
	if got.mainError != "" {
		t.Fatalf("expected inline error cleared, got %q", got.mainError)
	}
	if cmd == nil {
		t.Fatal("expected login modal Init cmd")
	}
}

func TestUpdateSendMessageFailedBusinessErrorKeepsSession(t *testing.T) {
	m := chatReadyModel(t)
	next, _ := m.Update(api.SendMessageFailed{
		Generation: m.sessionGeneration,
		HTTPStatus: http.StatusForbidden,
		Status:     "ROOM_NOT_MEMBER",
		Message:    "x",
	})
	got := next.(Model)
	if got.mainError != "Not a member of this room" {
		t.Fatalf("mainError = %q", got.mainError)
	}
	if got.conn == nil {
		t.Fatal("session torn down on a business-error send failure")
	}
	if got.modal != nil {
		t.Fatalf("modal opened on a business-error send failure: %T", got.modal)
	}
}

func TestSendMessageFailedRestoresComposerText(t *testing.T) {
	m := chatReadyModel(t)
	m.composer = newComposer(40) // empty after the optimistic clear-on-send
	next, _ := m.Update(api.SendMessageFailed{
		RoomID:     "general",
		Generation: m.sessionGeneration,
		HTTPStatus: http.StatusForbidden,
		Status:     "ROOM_NOT_MEMBER",
		Text:       "unsent words",
	})
	got := next.(Model)
	if got.composer.Value() != "unsent words" {
		t.Fatalf("composer not restored after a failed send: %q", got.composer.Value())
	}
	if got.mainError != "Not a member of this room" {
		t.Fatalf("mainError = %q", got.mainError)
	}
}

func TestSendMessageFailedDoesNotClobberNewerText(t *testing.T) {
	m := chatReadyModel(t)
	m.composer = newComposer(40)
	m.composer.SetValue("already typing this")
	next, _ := m.Update(api.SendMessageFailed{
		RoomID:     "general",
		Generation: m.sessionGeneration,
		HTTPStatus: http.StatusForbidden,
		Status:     "ROOM_NOT_MEMBER",
		Text:       "unsent words",
	})
	if got := next.(Model).composer.Value(); got != "already typing this" {
		t.Fatalf("restore clobbered text typed since the send: %q", got)
	}
}

func TestSendMessageFailedNoRestoreForOtherRoom(t *testing.T) {
	m := chatReadyModel(t) // active room is "general"
	m.composer = newComposer(40)
	next, _ := m.Update(api.SendMessageFailed{
		RoomID:     "random",
		Generation: m.sessionGeneration,
		HTTPStatus: http.StatusForbidden,
		Status:     "ROOM_NOT_MEMBER",
		Text:       "unsent words",
	})
	if got := next.(Model).composer.Value(); got != "" {
		t.Fatalf("restored text into the wrong room's composer: %q", got)
	}
}

func TestSendMessageFailedUnauthorizedDoesNotRestore(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.composer = newComposer(40)
	next, _ := m.Update(api.SendMessageFailed{
		RoomID:     "general",
		Generation: m.sessionGeneration,
		HTTPStatus: http.StatusUnauthorized,
		Status:     "AUTH_EXPIRED_TOKEN",
		Text:       "unsent words",
	})
	got := next.(Model)
	if got.composer.Value() != "" {
		t.Fatalf("the 401 path must not restore text; composer = %q", got.composer.Value())
	}
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("expected session expiry to reopen the login modal, got %T", got.modal)
	}
}

func TestUpdateSendMessageSucceededClearsError(t *testing.T) {
	m := chatReadyModel(t)
	m.mainError = "stale error"
	next, _ := m.Update(api.SendMessageSucceeded{RoomID: "general", Generation: m.sessionGeneration})
	if got := next.(Model).mainError; got != "" {
		t.Fatalf("mainError not cleared on success: %q", got)
	}
}

func TestUpdateSendMessageSucceededFromOldGenerationDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.sessionGeneration = 2
	m.mainError = "current session error"

	next, cmd := m.Update(api.SendMessageSucceeded{RoomID: "general", Generation: 1})
	got := next.(Model)

	if cmd != nil {
		t.Fatal("expected stale success to dispatch no command")
	}
	if got.mainError != "current session error" {
		t.Fatalf("mainError overwritten by stale success: %q", got.mainError)
	}
	if got.token == "" || got.conn == nil {
		t.Fatal("stale success tore down the current session")
	}
	if got.modal != nil {
		t.Fatalf("stale success opened a modal: %T", got.modal)
	}
}

func TestUpdateSendMessageFailedFromOldGenerationDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.sessionGeneration = 2
	m.mainError = "current session error"

	next, cmd := m.Update(api.SendMessageFailed{
		Generation: 1,
		HTTPStatus: http.StatusUnauthorized,
		Status:     "AUTH_EXPIRED_TOKEN",
	})
	got := next.(Model)

	if cmd != nil {
		t.Fatal("expected stale failure to dispatch no command")
	}
	if got.mainError != "current session error" {
		t.Fatalf("mainError overwritten by stale failure: %q", got.mainError)
	}
	if got.token == "" || got.conn == nil {
		t.Fatal("stale failure expired the current session")
	}
	if got.modal != nil {
		t.Fatalf("stale failure opened a modal: %T", got.modal)
	}
}

func TestUpdateWSDisconnectedFromOldGenerationDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.sessionGeneration = 2
	m.email = "rina@example.com"
	m.infoState.Email = "rina@example.com"
	m.messages["general"] = []mainview.Message{{Sender: "alice", Text: "current"}}
	m.mainError = "current session error"

	next, cmd := m.Update(api.WSDisconnected{Err: errors.New("lost"), Generation: 1})
	got := next.(Model)

	if cmd != nil {
		t.Fatal("expected stale disconnect to dispatch no command")
	}
	if got.token != "fake.jwt" || got.conn == nil {
		t.Fatal("stale disconnect tore down the current session")
	}
	if !got.connected {
		t.Fatal("stale disconnect cleared connected state")
	}
	if got.modal != nil {
		t.Fatalf("stale disconnect opened a modal: %T", got.modal)
	}
	if got.mainError != "current session error" {
		t.Fatalf("mainError overwritten by stale disconnect: %q", got.mainError)
	}
	if len(got.messages["general"]) != 1 {
		t.Fatalf("messages changed after stale disconnect: %#v", got.messages)
	}
}

func TestUpdateWSFrameReceivedFromOldGenerationDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.sessionGeneration = 2
	m.messages["general"] = []mainview.Message{{Sender: "alice", Text: "current"}}
	m.mainError = "current session error"

	next, cmd := m.Update(api.WSFrameReceived{
		Type:       "message",
		Raw:        messageFrameJSON("general", "bob", "stale", 2, 2000),
		Generation: 1,
	})
	got := next.(Model)

	if cmd != nil {
		t.Fatal("expected stale frame to dispatch no command")
	}
	if got.token != "fake.jwt" || got.conn == nil {
		t.Fatal("stale frame tore down the current session")
	}
	if got.mainError != "current session error" {
		t.Fatalf("mainError overwritten by stale frame: %q", got.mainError)
	}
	bucket := got.messages["general"]
	if len(bucket) != 1 || bucket[0].Text != "current" {
		t.Fatalf("stale frame mutated messages: %#v", bucket)
	}
	if got.modal != nil {
		t.Fatalf("stale frame opened a modal: %T", got.modal)
	}
}

func TestUpdateSendMessageCompletionAfterDisconnectAndReloginDropped(t *testing.T) {
	m := chatReadyModel(t)
	oldGeneration := m.sessionGeneration

	afterDisconnect, _ := m.Update(api.WSDisconnected{Err: errors.New("lost"), Generation: oldGeneration})
	afterLogin, _ := afterDisconnect.(Model).Update(api.LoginSucceeded{Token: "new.jwt", Email: "rina@example.com"})
	current := afterLogin.(Model)
	current.modal = nil
	current.conn = &websocket.Conn{}
	current.activeRoomID = "general"
	current.mainError = "current session error"

	next, cmd := current.Update(api.SendMessageFailed{
		Generation: oldGeneration,
		HTTPStatus: http.StatusUnauthorized,
		Status:     "AUTH_EXPIRED_TOKEN",
	})
	got := next.(Model)

	if cmd != nil {
		t.Fatal("expected stale completion to dispatch no command")
	}
	if got.sessionGeneration != oldGeneration+1 {
		t.Fatalf("sessionGeneration = %d, want %d", got.sessionGeneration, oldGeneration+1)
	}
	if got.token != "new.jwt" || got.conn == nil {
		t.Fatal("stale completion expired the relogged-in session")
	}
	if got.mainError != "current session error" {
		t.Fatalf("mainError overwritten by stale completion: %q", got.mainError)
	}
}

func TestUpdateWSAuthSucceededMarksConnectedAndInitsChat(t *testing.T) {
	m := makeModel(t)
	m.token = "fake.jwt"
	m.email = "rina@example.com"
	next, _ := m.Update(api.WSAuthSucceeded{UserID: "u-rina-1"})
	got := next.(Model)
	if !got.connected {
		t.Fatal("connected not set on WSAuthSucceeded")
	}
	if got.messages == nil {
		t.Fatal("message bucket map not initialised on WSAuthSucceeded")
	}
}

func TestHandleKeyEnterEmptyComposerNoSend(t *testing.T) {
	m := chatReadyModel(t)
	m.composer = newComposer(40) // empty value
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter with an empty composer must not dispatch a send cmd")
	}
}

func TestHandleKeyEnterWithTextDispatchesSendAndClears(t *testing.T) {
	m := chatReadyModel(t)
	m.composer = newComposer(40)
	m.composer.SetValue("hello there")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a send cmd for a non-empty composer with an active room")
	}
	if got := next.(Model).composer.Value(); got != "" {
		t.Fatalf("composer not cleared after send: %q", got)
	}
}

func TestHandleKeyEnterNoActiveRoomNoSend(t *testing.T) {
	m := chatReadyModel(t)
	m.activeRoomID = ""
	m.composer = newComposer(40)
	m.composer.SetValue("hello")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter with no active room must not dispatch a send cmd")
	}
}

func TestHandleKeyEnteringInputFocusesComposer(t *testing.T) {
	m := chatReadyModel(t)
	m.focus = focusMainView
	m.composer = newComposer(40)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got := next.(Model)
	if got.focus != focusMainInput {
		t.Fatalf("focus = %v, want focusMainInput", got.focus)
	}
	if cmd == nil {
		t.Fatal("expected the composer blink cmd on entering the input region")
	}
	if !got.composer.Focused() {
		t.Fatal("composer not focused on entering the input region")
	}
}

func TestHandleKeySlashLiteralWhenComposerFocused(t *testing.T) {
	m := chatReadyModel(t)
	m.composer = newComposer(40)
	m.composer.Focus()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	got := next.(Model)
	if got.modal != nil {
		t.Fatalf("/ opened a modal while the composer was focused: %T", got.modal)
	}
	if got.composer.Value() != "/" {
		t.Fatalf("/ not inserted into the composer: %q", got.composer.Value())
	}
}

func TestHandleKeyRoomSwitchClearsInlineError(t *testing.T) {
	m := chatReadyModel(t)
	m.focus = focusRooms
	m.roomsState.JoinedIDs = []string{"general", "random"}
	m.roomsState.Cursor = 1
	m.mainError = "stale"
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if got.activeRoomID != "random" {
		t.Fatalf("activeRoomID = %q, want random", got.activeRoomID)
	}
	if got.mainError != "" {
		t.Fatalf("mainError not cleared on room switch: %q", got.mainError)
	}
}
