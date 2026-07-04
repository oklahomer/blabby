package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/createroom"
	"github.com/oklahomer/blabby/cmd/client/internal/roomsearch"
)

func TestCreateRoomRequestedOpensCreateRoomModal(t *testing.T) {
	m := chatReadyModel(t)
	m.modal = m.openRoomSearchModal()

	next, _ := m.Update(roomsearch.CreateRoomRequested{})
	if _, ok := next.(Model).modal.(createroom.Model); !ok {
		t.Fatalf("modal = %T, want createroom.Model", next.(Model).modal)
	}
}

func TestCreateRoomCancelledReturnsToSearch(t *testing.T) {
	m := chatReadyModel(t)
	m.modal = createroom.New(m.createRoomSubmitter(), m.server.String())

	next, _ := m.Update(createroom.Cancelled{})
	if _, ok := next.(Model).modal.(roomsearch.Model); !ok {
		t.Fatalf("modal = %T, want roomsearch.Model", next.(Model).modal)
	}
}

func TestRoomCreatedActivatesRoomAndReloads(t *testing.T) {
	m := chatReadyModel(t)
	m.modal = createroom.New(m.createRoomSubmitter(), m.server.String())

	next, cmd := m.Update(api.RoomCreated{Room: api.Room{ID: "RK000000042", Name: "Team Standup"}})
	got := next.(Model)
	if got.modal != nil {
		t.Fatalf("modal = %T, want closed", got.modal)
	}
	if got.activeRoomID != "RK000000042" || got.mainviewState.RoomLabel != "Team Standup" {
		t.Fatalf("active room = %q label=%q", got.activeRoomID, got.mainviewState.RoomLabel)
	}
	if got.nameForID["RK000000042"] != "Team Standup" {
		t.Fatalf("name not cached: %#v", got.nameForID)
	}
	if cmd == nil {
		t.Fatal("expected a joined-rooms reload cmd")
	}
}

func TestRoomCreatedAfterSessionEndIsDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.token = ""
	m.conn = nil

	next, cmd := m.Update(api.RoomCreated{Room: api.Room{ID: "RK000000042", Name: "Team Standup"}})
	got := next.(Model)
	if got.activeRoomID == "RK000000042" || cmd != nil {
		t.Fatal("a create completing after session end must change nothing")
	}
}

func TestLeaveKeyDispatchesAfterConfirm(t *testing.T) {
	m := chatReadyModel(t)
	m.focus = focusRooms
	m.roomsState.JoinedIDs = []string{"general"}
	m.roomsState.NameForID = map[string]string{"general": "General"}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("first x must only arm the confirmation")
	}
	if m.roomsState.PendingLeaveID != "general" {
		t.Fatalf("pending = %q, want general", m.roomsState.PendingLeaveID)
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if cmd == nil {
		t.Fatal("second x must dispatch the leave cmd")
	}
}

func TestRoomLeftClearsActiveRoomAndReloads(t *testing.T) {
	m := chatReadyModel(t)
	m.roomsState.JoinedIDs = []string{"general"}

	next, cmd := m.Update(api.RoomLeft{RoomID: "general", RoomName: "General"})
	got := next.(Model)
	if got.activeRoomID != "" || got.mainviewState.RoomLabel != "" {
		t.Fatalf("active room not cleared: %q/%q", got.activeRoomID, got.mainviewState.RoomLabel)
	}
	if cmd == nil {
		t.Fatal("expected a joined-rooms reload cmd")
	}
}

func TestRoomLeftForInactiveRoomKeepsActive(t *testing.T) {
	m := chatReadyModel(t)
	m.activeRoomID = "random"
	m.mainviewState.RoomLabel = "Random"

	next, _ := m.Update(api.RoomLeft{RoomID: "general", RoomName: "General"})
	got := next.(Model)
	if got.activeRoomID != "random" || got.mainviewState.RoomLabel != "Random" {
		t.Fatal("leaving a background room must not clear the active room")
	}
}

func TestRoomLeaveFailedRendersActionError(t *testing.T) {
	m := chatReadyModel(t)

	next, _ := m.Update(api.RoomLeaveFailed{
		RoomID: "general", Status: "ROOM_OWNER_CANNOT_LEAVE",
		Message: "transfer ownership before leaving the room", HTTPStatus: 409,
	})
	got := next.(Model)
	if !strings.Contains(got.roomsState.ActionError, "Transfer ownership") {
		t.Fatalf("ActionError = %q", got.roomsState.ActionError)
	}
}

func TestRoomLeaveFailedUnauthorizedExpiresSession(t *testing.T) {
	m := chatReadyModel(t)

	next, _ := m.Update(api.RoomLeaveFailed{RoomID: "general", Status: "AUTH_EXPIRED_TOKEN", HTTPStatus: 401})
	got := next.(Model)
	if got.token != "" {
		t.Fatal("session not expired on 401 leave failure")
	}
}
