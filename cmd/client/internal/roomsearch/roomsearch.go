// Package roomsearch implements the search-and-join modal opened by
// the `/` keystroke in the TUI client. The modal owns the filter
// textinput, the in-flight phase enum, and the inline error rows;
// transport is injected as Submitter / Loader closures so the
// package stays free of HTTP concerns and remains trivially testable.
package roomsearch

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/modal"
	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

const (
	// modalWidth is the width of the rendered search modal box, in
	// columns. Matches the login modal's visual weight while giving
	// the two-column room list comfortable room.
	modalWidth = 56

	// nameColumnWidth caps the rendered width of the Name column so
	// the ID column lines up cleanly. Beyond this width names are
	// truncated; truncation is acceptable in Phase 1 where the
	// catalogue holds two two-character names.
	nameColumnWidth = 24
)

// phase enumerates the modal's three states. phaseLoading hides the
// list until the catalogue arrives; phaseIdle is the keyboard-driven
// browse state; phaseJoining renders the in-flight footer and
// suppresses every key except ctrl+c (which the chrome owns).
type phase int

const (
	phaseLoading phase = iota
	phaseIdle
	phaseJoining
)

// Submitter is the function the modal calls when the user presses
// enter on a non-empty visible list. The closure wires in
// api.JoinRoomCmd from the root Model — the same seam login uses for
// api.LoginCmd.
type Submitter func(roomID, roomName string) tea.Cmd

// Loader is the function the modal calls on Init (and after a
// transport-failed first load, in a future iteration) to dispatch
// api.LoadRoomsCmd. Kept as a separate type from Submitter because
// the two have different signatures and different call sites; mixing
// them would force one to wrap the other unnecessarily.
type Loader func() tea.Cmd

// Model is the search modal state. It implements modal.Modal.
type Model struct {
	filter      textinput.Model // focused; placeholder "filter…"
	all         []api.Room      // last-loaded catalogue (empty until RoomsLoaded arrives)
	cursor      int             // index into the filtered (visible) view, not all
	phase       phase
	headline    string // inline error row (above the footer)
	detail      string // second line of error (transport reason in parens)
	server      string // for the "Cannot reach server at {server}" headline
	submit      Submitter
	loadAgain   Loader
	joiningName string // captured at enter time for the in-flight footer copy
}

// New constructs a Model with the filter textinput focused and the
// loading phase active. server is rendered into the
// "Cannot reach server at {server}" headline so the user sees which
// endpoint the client could not reach.
func New(submit Submitter, load Loader, server string) Model {
	input := textinput.New()
	input.Placeholder = "filter…"
	input.Prompt = ""
	input.Width = modalWidth - 12
	input.CharLimit = 256
	input.Focus()
	return Model{
		filter:    input,
		phase:     phaseLoading,
		server:    server,
		submit:    submit,
		loadAgain: load,
	}
}

// Init returns a batch that starts the textinput cursor blink and
// dispatches the initial LoadRoomsCmd via the injected loader.
// Implements modal.Modal.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.loadAgain())
}

// Update routes incoming messages through the modal's dispatch
// table. Key events flow into handleKey; transport-outcome messages
// from the root Model drive the phase transitions and inline error
// rendering. Implements modal.Modal.
func (m Model) Update(msg tea.Msg) (modal.Modal, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case api.RoomsLoaded:
		m.all = v.Rooms
		m.cursor = 0
		m.phase = phaseIdle
		m.headline = ""
		m.detail = ""
		return m, nil
	case api.RoomsLoadFailed:
		m.phase = phaseIdle
		m.applyFailure(v.Status, v.Message, v.HTTPStatus)
		return m, nil
	case api.RoomJoined:
		// The root Model also handles RoomJoined to close the modal
		// and update active-room state. Returning nil here is the
		// single-modal protocol's dismiss signal — Update returning
		// nil means "I'm done."
		return nil, nil
	case api.RoomJoinFailed:
		m.phase = phaseIdle
		m.applyFailure(v.Status, v.Message, v.HTTPStatus)
		return m, nil
	}

	updated, cmd := m.filter.Update(msg)
	m.filter = updated
	m.clampCursor()
	return m, cmd
}

// View renders the modal box. width and height are the full screen
// dimensions; the chrome supplies the centring via modal.Overlay.
// Implements modal.Modal.
func (m Model) View(_, _ int) string {
	visible := Visible(m.all, m.filter.Value())

	body := []string{
		ui.Title().Render("Search rooms"),
		"",
		ui.Label().Render("filter:  ") + m.filter.View(),
		"",
	}
	body = append(body, m.renderListBody(visible)...)
	if m.headline != "" {
		body = append(body, "", ui.Error().Render("✗ "+m.headline))
		if m.detail != "" {
			body = append(body, ui.Subtle().Render("  "+m.detail))
		}
	}
	body = append(body, "", m.renderFooter())

	return ui.ModalBorder().Width(modalWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, body...),
	)
}

// handleKey routes a key event through the modal's key precedence
// rules. While joining, every key is suppressed (ctrl+c is owned by
// the chrome). esc dismisses; ctrl+1/2/3 are absorbed; movement keys
// step the cursor; enter dispatches a submit; everything else flows
// into the filter textinput.
func (m Model) handleKey(k tea.KeyMsg) (modal.Modal, tea.Cmd) {
	if m.phase == phaseJoining {
		return m, nil
	}
	switch k.String() {
	case "esc":
		return nil, nil
	case "ctrl+1", "ctrl+2", "ctrl+3":
		// Pane-focus shortcuts have a global meaning when no modal is
		// open; while the modal is open they must be absorbed
		// silently so they neither switch background pane focus nor
		// leak into the textinput.
		return m, nil
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down":
		visible := Visible(m.all, m.filter.Value())
		if m.cursor < len(visible)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		visible := Visible(m.all, m.filter.Value())
		if len(visible) == 0 {
			return m, nil
		}
		row := visible[m.cursor]
		m.phase = phaseJoining
		m.joiningName = row.Name
		m.headline = ""
		m.detail = ""
		return m, m.submit(row.ID, row.Name)
	}

	updated, cmd := m.filter.Update(k)
	m.filter = updated
	m.clampCursor()
	return m, cmd
}

// applyFailure maps a server-side or transport failure into the
// modal's headline / detail rows. The status mapping uses
// api.Humanise; transport errors (HTTPStatus == 0) get the same
// "Cannot reach server at {server}" treatment the login modal uses.
func (m *Model) applyFailure(status, message string, httpStatus int) {
	if status == "" && httpStatus == 0 {
		m.headline = "Cannot reach server at " + m.server
		m.detail = "(" + message + ")"
		return
	}
	m.headline = api.Humanise(status, message)
	m.detail = ""
}

// clampCursor pins the cursor inside [0, len(visible)-1] so a
// narrowing filter never leaves it pointing past the last visible row.
// Called after every filter mutation.
func (m *Model) clampCursor() {
	visible := Visible(m.all, m.filter.Value())
	if len(visible) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
		return
	}
	if m.cursor > len(visible)-1 {
		m.cursor = len(visible) - 1
	}
}

// renderListBody picks the right body for the current phase / filter
// state. Mutually exclusive branches in order: loading → empty
// (no-filter vs filtered) → two-column populated render.
func (m Model) renderListBody(visible []api.Room) []string {
	if m.phase == phaseLoading {
		return []string{ui.Subtle().Render("(loading…)")}
	}
	if len(visible) == 0 {
		if m.filter.Value() == "" {
			return []string{ui.Subtle().Render("(no rooms available)")}
		}
		return []string{ui.Subtle().Render("(no rooms match this filter)")}
	}

	rows := make([]string, 0, len(visible))
	for i, r := range visible {
		var nameCell string
		if i == m.cursor {
			nameCell = ui.Title().Width(nameColumnWidth).Render("› " + r.Name)
		} else {
			nameCell = ui.Subtle().Width(nameColumnWidth).Render("  " + r.Name)
		}
		rows = append(rows, nameCell+ui.Subtle().Render(r.ID))
	}
	return rows
}

// renderFooter picks the footer line. The join phase shows progress;
// idle / loading both show the same key glossary.
func (m Model) renderFooter() string {
	if m.phase == phaseJoining {
		return ui.Subtle().Render("Joining " + m.joiningName + "…")
	}
	return ui.Subtle().Render("↑↓: navigate · enter: join · esc: cancel")
}
