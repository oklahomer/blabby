package app

import (
	"net/http"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// updateChat handles the chat/backfill message family: timeline history
// pages, live WebSocket frames, and message-send completions. It reports
// handled=false for anything outside the family so Update can probe the
// next one.
func (m Model) updateChat(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch v := msg.(type) {
	case api.RoomEventsLoaded:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		oldActive := m.activeLines()
		if m.histories == nil {
			m.histories = map[string]roomHistory{}
		}
		h := m.histories[v.RoomID]
		prevFetched := h.fetched
		h.loading = false
		h.fetched = true
		// Advance the pagination cursor: an older page (before != "") always
		// adopts the server's next cursor, while a newest-page load seeds it
		// only on the first fetch so a re-activation refresh cannot rewind a
		// scroll-up's deeper cursor. The cursor is set before the merge so a
		// trim during the merge (the fetched region overflowing the cap)
		// re-heals it to the surviving oldest entry.
		if v.Before != "" || !prevFetched {
			h.next = v.Next
			h.exhausted = v.Next == ""
		}
		m.histories[v.RoomID] = h
		for _, ev := range v.Events {
			m = m.appendTimelineEvent(v.RoomID, ev)
		}
		m = m.reanchor(oldActive)
		return m, nil, true

	case api.RoomEventsLoadFailed:
		if !m.liveRoomResult(v.Generation) {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		m = m.setHistoryLoading(v.RoomID, false)
		if v.RoomID == m.activeRoomID {
			m.mainError = api.Humanise(v.Status, v.Message)
		}
		return m, nil, true

	case api.WSFrameReceived:
		if v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		// message, joined, and left frames render into the active room's
		// scrollback; error sets the inline Main-pane error; ping is
		// delivered but not yet answered (the pong reply is parked with the
		// heartbeat arc). Each decoder fails closed, dropping a frame the
		// server sent malformed or without an event id. A scrolled-up view
		// is re-anchored so an inbound frame does not slide it.
		oldActive := m.activeLines()
		switch v.Type {
		case "message":
			if cm, ok := api.DecodeChatMessage(v.Raw); ok {
				m = m.appendChatMessage(cm)
			}
		case "joined", "left":
			if me, ok := api.DecodeMemberEvent(v.Raw); ok {
				m = m.appendMemberEvent(me)
			}
		case "error":
			if ef, ok := api.DecodeErrorFrame(v.Raw); ok {
				m.mainError = api.Humanise(ef.Status, ef.Message)
			}
		}
		m = m.reanchor(oldActive)
		return m, nil, true

	case api.SendMessageSucceeded:
		if m.token == "" || m.conn == nil || v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		// The sent message renders when its echo frame arrives over the
		// WebSocket, not here; the HTTP 200 only clears any prior error.
		m.mainError = ""
		return m, nil, true

	case api.SendMessageFailed:
		if m.token == "" || m.conn == nil || v.Generation != m.sessionGeneration {
			return m, nil, true
		}
		if v.HTTPStatus == http.StatusUnauthorized {
			next, cmd := m.handleSessionExpiry()
			return next, cmd, true
		}
		// Restore the attempted text so the user can retry without retyping —
		// only into the still-empty composer of the room it was sent from, so
		// it never clobbers text typed since nor lands in another room.
		if v.RoomID == m.activeRoomID && strings.TrimSpace(m.composer.Value()) == "" {
			m.composer.SetValue(v.Text)
		}
		m.mainError = api.Humanise(v.Status, v.Message)
		return m, nil, true
	}
	return m, nil, false
}
