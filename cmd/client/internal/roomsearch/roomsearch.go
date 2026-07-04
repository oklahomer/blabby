// Package roomsearch implements the search-and-join modal opened by
// the `/` keystroke in the TUI client. The modal owns the filter
// textinput, the in-flight phase enum, and the inline error rows;
// transport is injected as Submitter / Loader closures so the
// package stays free of HTTP concerns and remains trivially testable.
//
// Filtering is two-layered: typing narrows the already-loaded page
// instantly (client-side substring), while a debounce re-queries the
// server with the fragment as `q`, replacing the list so matches
// beyond the loaded page appear. A "more rooms" row pages the
// catalogue through the server's keyset cursor.
package roomsearch

import (
	"strings"
	"time"

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
	// truncated.
	nameColumnWidth = 24

	// searchDebounce is how long typing must pause before the filter
	// fragment is sent to the server as `q`. Local narrowing is
	// instant; the debounce only gates the network round-trip.
	searchDebounce = 300 * time.Millisecond

	// pageStep is how many rows pgup/pgdn move the cursor.
	pageStep = 10
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
// enter on a room row. The closure wires in api.JoinRoomCmd from the
// root Model — the same seam login uses for api.LoginCmd.
type Submitter func(roomID, roomName string) tea.Cmd

// Loader is the function the modal calls to fetch one catalogue page:
// on Init (zero query), when the search debounce fires (fragment as
// Query), and when the user selects the "more rooms" row (Query plus
// the After cursor).
type Loader func(query api.RoomQuery) tea.Cmd

// CreateRoomRequested is the typed outcome emitted when the user
// presses ctrl+n: the root Model maps it to the create-room modal.
type CreateRoomRequested struct{}

// searchTick fires when a search debounce window elapses. seq guards
// against stale timers: only the tick matching the latest keystroke's
// sequence number dispatches a server query.
type searchTick struct {
	seq int
}

// Model is the search modal state. It implements modal.Modal.
type Model struct {
	filter      textinput.Model // focused; placeholder "filter…"
	all         []api.Room      // last-loaded server page(s) for loadedQuery
	loadedQuery string          // the fragment all was fetched with — the listing's identity
	next        string          // server cursor for the next page ("" = exhausted)
	cursor      int             // index into the row domain: visible rooms + the more row
	phase       phase
	debounceSeq int    // latest keystroke sequence; stale searchTicks are dropped
	loadingMore bool   // a more-row page request is in flight
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
// dispatches the initial first-page load via the injected loader.
// Implements modal.Modal.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.loadAgain(api.RoomQuery{}))
}

// Update routes incoming messages through the modal's dispatch
// table. Key events flow into handleKey; transport-outcome messages
// from the root Model drive the phase transitions and inline error
// rendering. Implements modal.Modal.
func (m Model) Update(msg tea.Msg) (modal.Modal, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case searchTick:
		// Only the newest debounce window may query; a tick armed by an
		// earlier keystroke is stale.
		if v.seq != m.debounceSeq {
			return m, nil
		}
		return m, m.loadAgain(api.RoomQuery{Query: m.trimmedFilter()})
	case api.RoomsLoaded:
		if v.After != "" {
			// An append belongs to the listing on screen only if it continues
			// the same fragment at the current cursor. Anything else — the
			// user typed past it, or a first-page replace landed while the
			// page request was in flight — is a stale page whose rooms would
			// duplicate or gap the fresh list.
			if v.Query != m.loadedQuery || v.After != m.next {
				m.loadingMore = false
				return m, nil
			}
			m.all = append(m.all, v.Rooms...)
		} else {
			// A first page for a fragment the user has typed past is stale:
			// results for the current fragment are already on their way.
			if v.Query != m.trimmedFilter() {
				return m, nil
			}
			m.all = v.Rooms
			m.loadedQuery = v.Query
			m.cursor = 0
		}
		m.next = v.Next
		m.phase = phaseIdle
		m.loadingMore = false
		m.headline = ""
		m.detail = ""
		m.clampCursor()
		return m, nil
	case api.RoomsLoadFailed:
		m.phase = phaseIdle
		m.loadingMore = false
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
// the chrome). esc dismisses; ctrl+n asks for the create-room modal;
// ctrl+1/2/3 are absorbed; movement keys step the cursor across the
// visible rooms plus the "more rooms" row; enter joins the selected
// room or fetches the next page; everything else flows into the
// filter textinput and (when the fragment changed) re-arms the search
// debounce.
func (m Model) handleKey(k tea.KeyMsg) (modal.Modal, tea.Cmd) {
	if m.phase == phaseJoining {
		return m, nil
	}
	visible := Visible(m.all, m.filter.Value())
	switch k.String() {
	case "esc":
		return nil, nil
	case "ctrl+n":
		return m, func() tea.Msg { return CreateRoomRequested{} }
	case "ctrl+1", "ctrl+2", "ctrl+3":
		// Pane-focus shortcuts have a global meaning when no modal is
		// open; while the modal is open they must be absorbed
		// silently so they neither switch background pane focus nor
		// leak into the textinput.
		return m, nil
	case "up":
		return m.moveCursor(-1, visible), nil
	case "down":
		return m.moveCursor(1, visible), nil
	case "pgup":
		return m.moveCursor(-pageStep, visible), nil
	case "pgdown":
		return m.moveCursor(pageStep, visible), nil
	case "home":
		m.cursor = 0
		return m, nil
	case "end":
		m.cursor = m.lastRow(visible)
		return m, nil
	case "enter":
		if m.onMoreRow(visible) {
			// Paging is a continuation of the listing on screen, so it fetches
			// with loadedQuery, not the live filter. When the user has typed
			// past the loaded fragment, a debounced replace is already on its
			// way and paging the outgoing listing would only produce a page
			// the RoomsLoaded guard drops.
			if m.loadingMore || m.trimmedFilter() != m.loadedQuery {
				return m, nil
			}
			m.loadingMore = true
			return m, m.loadAgain(api.RoomQuery{Query: m.loadedQuery, After: m.next})
		}
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

	before := m.trimmedFilter()
	updated, cmd := m.filter.Update(k)
	m.filter = updated
	m.clampCursor()
	if after := m.trimmedFilter(); after != before {
		// Re-arm the debounce: bump the sequence so any earlier pending
		// tick goes stale, and schedule the query for this fragment.
		m.debounceSeq++
		seq := m.debounceSeq
		return m, tea.Batch(cmd, tea.Tick(searchDebounce, func(time.Time) tea.Msg {
			return searchTick{seq: seq}
		}))
	}
	return m, cmd
}

// trimmedFilter is the fragment the server queries run with: the
// filter field's value without surrounding whitespace.
func (m Model) trimmedFilter() string {
	return strings.TrimSpace(m.filter.Value())
}

// moveCursor steps the cursor by delta across the row domain (visible
// rooms plus the more row), clamping at both ends.
func (m Model) moveCursor(delta int, visible []api.Room) Model {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if last := m.lastRow(visible); m.cursor > last {
		m.cursor = last
	}
	return m
}

// lastRow is the highest cursor index: the last visible room, or the
// "more rooms" row when the server has another page.
func (m Model) lastRow(visible []api.Room) int {
	last := len(visible) - 1
	if m.next != "" {
		last++
	}
	if last < 0 {
		return 0
	}
	return last
}

// onMoreRow reports whether the cursor sits on the "more rooms" row.
func (m Model) onMoreRow(visible []api.Room) bool {
	return m.next != "" && m.cursor == len(visible)
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

// clampCursor pins the cursor inside the row domain so a narrowing
// filter never leaves it pointing past the last row. Called after
// every filter mutation and page arrival.
func (m *Model) clampCursor() {
	visible := Visible(m.all, m.filter.Value())
	if m.cursor < 0 {
		m.cursor = 0
		return
	}
	if last := m.lastRow(visible); m.cursor > last {
		m.cursor = last
	}
}

// renderListBody picks the right body for the current phase / filter
// state. Mutually exclusive branches in order: loading → empty
// (no-filter vs filtered) → two-column populated render plus the
// "more rooms" row when another server page exists.
func (m Model) renderListBody(visible []api.Room) []string {
	if m.phase == phaseLoading {
		return []string{ui.Subtle().Render("(loading…)")}
	}
	if len(visible) == 0 && m.next == "" {
		if m.filter.Value() == "" {
			return []string{ui.Subtle().Render("(no rooms available · ctrl+n: create room)")}
		}
		return []string{ui.Subtle().Render("(no rooms match this filter · ctrl+n: create room)")}
	}

	rows := make([]string, 0, len(visible)+1)
	for i, r := range visible {
		var nameCell string
		if i == m.cursor {
			nameCell = ui.Title().Width(nameColumnWidth).Render("› " + r.Name)
		} else {
			nameCell = ui.Subtle().Width(nameColumnWidth).Render("  " + r.Name)
		}
		rows = append(rows, nameCell+ui.Subtle().Render(r.ID))
	}
	if m.next != "" {
		label := "  ↓ more rooms…"
		if m.onMoreRow(visible) {
			label = "› ↓ more rooms…"
		}
		if m.loadingMore {
			label += " (loading)"
		}
		rows = append(rows, ui.Subtle().Render(label))
	}
	return rows
}

// renderFooter picks the footer line. The join phase shows progress;
// idle / loading both show the same key glossary.
func (m Model) renderFooter() string {
	if m.phase == phaseJoining {
		return ui.Subtle().Render("Joining " + m.joiningName + "…")
	}
	return ui.Subtle().Render("↑↓ nav · enter: join · ctrl+n: create · esc: cancel")
}
