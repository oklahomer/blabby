// Package rooms renders the left-side Rooms pane. The pane shows the
// authenticated user's joined-rooms list, surfaces loading and error
// states, and reports key-driven user intent to the root Model as
// typed Outcomes. The root Model maps each Outcome to the next state
// transition and any tea.Cmd that needs to fire — pane code stays
// free of program control flow.
package rooms

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// cursorGlyph is rendered on the focused row when the joined list has
// at least one entry. Unfocused rows are indented by the same width so
// the column alignment stays consistent.
const cursorGlyph = "› "

// indent is the leading space used for non-cursor rows so they line up
// with the column occupied by cursorGlyph on the cursor row.
const indent = "  "

// pageStep is how many rows pgup/pgdn move the cursor.
const pageStep = 10

// State holds the rendering inputs for the Rooms pane. A zero State
// renders as the empty placeholder — Loading=false, LoadError="",
// JoinedIDs=nil, NameForID=nil.
type State struct {
	// JoinedIDs is the server-authoritative list returned by
	// GET /rooms/joined. Order matches the response body.
	JoinedIDs []string
	// Cursor is the index into JoinedIDs the user is currently on.
	// Clamped to 0 when the list is empty.
	Cursor int
	// Loading is true while a GET /rooms/joined is in flight.
	Loading bool
	// LoadError is non-empty when the last load failed. The string is
	// already humanised — render verbatim.
	LoadError string
	// NameForID maps room IDs to display names captured during this
	// session's joins. Rooms loaded from a prior session render with
	// the ID until the user joins them again in-session.
	NameForID map[string]string
	// PendingLeaveID is the room awaiting the second x of the
	// leave-confirmation gesture. Any key other than the confirming x
	// clears it.
	PendingLeaveID string
	// ActionError is the humanised outcome of the last failed
	// key-driven action (currently: leave). Cleared on the next key.
	ActionError string
}

// Outcome describes the side effect a single key press has produced.
// HandleKey is pure and never opens transport — the root Model maps
// each Outcome to the next state transition and tea.Cmd.
type Outcome int

const (
	// OutcomeNone means the pane state may have moved internally
	// (cursor up/down) but no side effect was requested.
	OutcomeNone Outcome = iota
	// OutcomeSwitchActiveRoom means the user pressed enter on a
	// non-empty list and wants the room under the cursor to become
	// the active room. Caller reads State.ActiveID() to learn which.
	OutcomeSwitchActiveRoom
	// OutcomeRetryLoad means the user pressed r while the pane was
	// in a load-error state and wants the joined-rooms list re-fetched.
	OutcomeRetryLoad
	// OutcomeLeaveRoom means the user confirmed the two-press leave
	// gesture on the room under the cursor. Caller reads
	// State.ActiveID() to learn which.
	OutcomeLeaveRoom
)

// HandleKey applies a single key press to the pane state. The
// function is pure — the caller receives the updated State and an
// Outcome describing what user intent (if any) the key surfaced.
//
// Movement keys clamp at the list boundaries (no wraparound). Enter
// on an empty list, r without a pending load error, and any unknown
// key all return OutcomeNone with the state otherwise unchanged.
// Leaving is a two-press gesture: the first x arms the confirmation
// for the room under the cursor, a second x on the same room confirms
// it, and any other key (including moving to another room) disarms it.
func HandleKey(state State, key string) (State, Outcome) {
	state.ActionError = ""
	if key != "x" {
		state.PendingLeaveID = ""
	}
	switch key {
	case "up", "k":
		if state.Cursor > 0 {
			state.Cursor--
		}
		return state, OutcomeNone
	case "down", "j":
		if state.Cursor < len(state.JoinedIDs)-1 {
			state.Cursor++
		}
		return state, OutcomeNone
	case "pgup":
		state.Cursor -= pageStep
		if state.Cursor < 0 {
			state.Cursor = 0
		}
		return state, OutcomeNone
	case "pgdown":
		state.Cursor += pageStep
		if last := len(state.JoinedIDs) - 1; state.Cursor > last {
			if last < 0 {
				last = 0
			}
			state.Cursor = last
		}
		return state, OutcomeNone
	case "home":
		state.Cursor = 0
		return state, OutcomeNone
	case "end":
		if last := len(state.JoinedIDs) - 1; last > 0 {
			state.Cursor = last
		}
		return state, OutcomeNone
	case "enter":
		if len(state.JoinedIDs) == 0 {
			return state, OutcomeNone
		}
		return state, OutcomeSwitchActiveRoom
	case "r":
		if state.LoadError == "" {
			return state, OutcomeNone
		}
		return state, OutcomeRetryLoad
	case "x":
		active := state.ActiveID()
		if active == "" {
			state.PendingLeaveID = ""
			return state, OutcomeNone
		}
		if state.PendingLeaveID == active {
			state.PendingLeaveID = ""
			return state, OutcomeLeaveRoom
		}
		state.PendingLeaveID = active
		return state, OutcomeNone
	}
	return state, OutcomeNone
}

// ActiveID returns the room ID under the cursor, or "" when the list
// is empty. The root Model uses it to populate Model.activeRoomID
// after OutcomeSwitchActiveRoom.
func (s State) ActiveID() string {
	if len(s.JoinedIDs) == 0 {
		return ""
	}
	if s.Cursor < 0 || s.Cursor >= len(s.JoinedIDs) {
		return ""
	}
	return s.JoinedIDs[s.Cursor]
}

// ResolveName returns the display name for roomID, falling back to
// the ID verbatim when no in-session name is cached. Used by the
// root Model when computing the Main pane's RoomLabel.
func (s State) ResolveName(roomID string) string {
	if name, ok := s.NameForID[roomID]; ok && name != "" {
		return name
	}
	return roomID
}

// View renders the pane content. focused is honoured only for the
// cursor glyph on the focused row when the joined list is populated;
// the border itself is drawn by the chrome.
func View(s State, focused bool, _, _ int) string {
	title := ui.Title().Render("Rooms")
	body := bodyLines(s, focused)
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title, ""}, body...)...)
}

// bodyLines builds the rows under the title for one of four states:
// loading, load-error, empty, or populated. The branches are mutually
// exclusive — loading takes precedence over an error, error over an
// empty list, empty over the populated render.
func bodyLines(s State, focused bool) []string {
	switch {
	case s.Loading:
		return []string{ui.Subtle().Render("(loading…)")}
	case s.LoadError != "":
		return []string{
			ui.Error().Render("(failed to load rooms)"),
			ui.Subtle().Render("(press r to retry)"),
		}
	case len(s.JoinedIDs) == 0:
		return []string{
			ui.Subtle().Render("(no rooms yet)"),
			ui.Subtle().Render("(press / to search)"),
		}
	}
	rows := make([]string, 0, len(s.JoinedIDs))
	for i, id := range s.JoinedIDs {
		label := s.ResolveName(id)
		prefix := indent
		if focused && i == s.Cursor {
			prefix = cursorGlyph
		}
		rows = append(rows, prefix+label)
	}
	if s.PendingLeaveID != "" {
		rows = append(rows, "",
			ui.Error().Render("press x again to leave"),
			ui.Error().Render(s.ResolveName(s.PendingLeaveID)))
	}
	if s.ActionError != "" {
		rows = append(rows, "", ui.Error().Render(s.ActionError))
	}
	return rows
}
