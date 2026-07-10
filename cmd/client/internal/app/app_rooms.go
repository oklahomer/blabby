package app

import (
	"net/http"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/createroom"
	"github.com/oklahomer/blabby/cmd/client/internal/roomsearch"
)

// updateRooms handles the room-lifecycle message family: the joined-rooms
// list, the search/create/join/leave flows, and their failure paths. It
// reports handled=false for anything outside the family so Update can probe
// the next one.
func (m Model) updateRooms(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {
	case api.JoinedRoomsLoaded:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		// Descriptors carry the name alongside the id, so the Rooms pane shows
		// real names after a reload without relying on an in-session capture.
		if m.nameForID == nil {
			m.nameForID = map[string]string{}
		}
		ids := make([]string, len(v.Rooms))
		for i, room := range v.Rooms {
			ids[i] = room.ID
			m.nameForID[room.ID] = room.Name
		}
		m.roomsState.JoinedIDs = ids
		m.roomsState.NameForID = m.nameForID
		m.roomsState.Loading = false
		m.roomsState.LoadError = ""
		// A reload replaces the list a half-armed leave gesture referred to;
		// disarm it so the confirm banner cannot name a stale room.
		m.roomsState.PendingLeaveID = ""
		m.roomsState = m.roomsState.ClampCursor()
		return m, nil, true

	case api.JoinedRoomsLoadFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		m.roomsState.Loading = false
		m.roomsState.LoadError = api.Humanise(v.Status, v.Message)
		return m, nil, true

	case api.RoomJoined:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if m.nameForID == nil {
			m.nameForID = map[string]string{}
		}
		m.nameForID[v.RoomID] = v.RoomName
		m.roomsState.NameForID = m.nameForID
		m = m.activateRoom(v.RoomID, v.RoomName)
		m.modal = nil
		// Reload from the server so the Rooms pane reflects the
		// authoritative membership (the modal's optimistic-add does not
		// own this state), and backfill the newly active room's timeline.
		var joinedBackfill tea.Cmd
		m, joinedBackfill = m.beginBackfill(v.RoomID)
		return m, tea.Batch(
			api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout),
			joinedBackfill,
		), true

	case roomsearch.CreateRoomRequested:
		if !m.hasLiveSession() {
			return m, nil, true
		}
		if _, ok := m.modal.(roomsearch.Model); !ok {
			return m, nil, true
		}
		m.modal = createroom.New(m.createRoomSubmitter(), m.server.String())
		return m, m.modal.Init(), true

	case createroom.Cancelled:
		if !m.hasLiveSession() {
			return m, nil, true
		}
		if _, ok := m.modal.(createroom.Model); !ok {
			return m, nil, true
		}
		m.modal = m.openRoomSearchModal()
		return m, m.modal.Init(), true

	case api.RoomCreated:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		// The server seeded the caller as the room's owner, so this is a
		// completed join: activate the room and reload the authoritative list.
		if m.nameForID == nil {
			m.nameForID = map[string]string{}
		}
		m.nameForID[v.Room.ID] = v.Room.Name
		m.roomsState.NameForID = m.nameForID
		m = m.activateRoom(v.Room.ID, v.Room.Name)
		m.modal = nil
		var createdBackfill tea.Cmd
		m, createdBackfill = m.beginBackfill(v.Room.ID)
		return m, tea.Batch(
			api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout),
			createdBackfill,
		), true

	case api.RoomCreateFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd, true
		}
		return m, nil, true

	case api.RoomLeft:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if m.activeRoomID == v.RoomID {
			// The active room is gone from the user's set; the Main pane
			// returns to its no-room placeholder.
			m.activeRoomID = ""
			m.mainviewState.RoomLabel = ""
			m.mainError = ""
		}
		m.roomsState.ActionError = ""
		return m, api.LoadJoinedRoomsCmd(m.httpClient, m.server.String(), m.token, m.sessionGeneration, api.DefaultRoomCallTimeout), true

	case api.RoomLeaveFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		m.roomsState.ActionError = api.Humanise(v.Status, v.Message)
		return m, nil, true

	case api.RoomsLoaded:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd, true
		}
		return m, nil, true

	case api.RoomsLoadFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd, true
		}
		return m, nil, true

	case api.RoomJoinFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		if m.modal != nil {
			next, cmd := m.modal.Update(msg)
			m.modal = next
			return m, cmd, true
		}
		return m, nil, true
	}
	return m, nil, false
}
