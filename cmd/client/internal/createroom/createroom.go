// Package createroom defines the room-creation modal reached from the search
// modal via ctrl+n. It implements modal.Modal, owns the single name field, and
// reports its terminal outcomes as typed messages: the root Model activates
// the new room on api.RoomCreated and returns to the search modal on
// Cancelled — the transition itself stays in the root state machine.
package createroom

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/modal"
	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

const (
	// modalWidth is the width of the rendered modal box, in columns.
	modalWidth = 54

	// maxNameBytes mirrors the server's room-name byte cap
	// (internal/domain.MaxRoomNameBytes); re-checking at submit surfaces a
	// friendlier error than the round-tripped envelope.
	maxNameBytes = 64
)

// Submitter is the function the modal calls when the user presses enter with
// a valid name. It is the seam through which the root Model wires in
// api.CreateRoomCmd, keeping this package free of HTTP concerns.
type Submitter func(name string) tea.Cmd

// Cancelled is the typed outcome emitted when the user presses esc: the root
// Model maps it back to the search modal.
type Cancelled struct{}

// Model is the create-room modal state. It satisfies modal.Modal.
type Model struct {
	name     textinput.Model
	creating bool
	headline string
	detail   string
	server   string
	submit   Submitter
}

// New constructs a Model with the name field focused.
func New(submit Submitter, server string) Model {
	name := textinput.New()
	name.Placeholder = "room name"
	name.Prompt = ""
	name.Width = modalWidth - 12
	name.CharLimit = maxNameBytes
	name.Focus()

	return Model{
		name:   name,
		server: server,
		submit: submit,
	}
}

// Init returns the textinput blink cmd so the name field shows a cursor.
// Implements modal.Modal.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles key events and transport-outcome messages from the root
// Model. api.RoomCreated never reaches this modal — the root activates the
// new room on it. Implements modal.Modal.
func (m Model) Update(msg tea.Msg) (modal.Modal, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case api.RoomCreateFailed:
		if v.Status == "" && v.HTTPStatus == 0 {
			return m.showError("Cannot reach server at "+m.server, "("+v.Message+")"), nil
		}
		return m.showError(api.Humanise(v.Status, v.Message), ""), nil
	}

	updated, cmd := m.name.Update(msg)
	m.name = updated
	return m, cmd
}

// View renders the modal box. Implements modal.Modal.
func (m Model) View(_, _ int) string {
	body := []string{
		ui.Title().Render("Create a room"),
		"",
		ui.Label().Render("Name:  ") + m.name.View(),
	}

	if m.headline != "" {
		body = append(body, "", ui.Error().Render("✗ "+m.headline))
		if m.detail != "" {
			body = append(body, ui.Subtle().Render("  "+m.detail))
		}
	}

	body = append(body, "")
	if m.creating {
		body = append(body, ui.Subtle().Render("Creating room…"))
	} else {
		body = append(body, ui.Subtle().Render("enter: create · esc: back to search"))
	}

	return ui.ModalBorder().Width(modalWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, body...),
	)
}

// handleKey routes a key event through the submit / esc / in-flight rules.
func (m Model) handleKey(k tea.KeyMsg) (modal.Modal, tea.Cmd) {
	if m.creating {
		// ctrl+c is handled at the root level; every other key is suppressed
		// while we wait for the server.
		return m, nil
	}

	switch k.String() {
	case "ctrl+1", "ctrl+2", "ctrl+3":
		return m, nil
	case "esc":
		return m, func() tea.Msg { return Cancelled{} }
	case "enter":
		name := strings.TrimSpace(m.name.Value())
		if headline, ok := validateName(name); !ok {
			m.headline = headline
			m.detail = ""
			return m, nil
		}
		m.creating = true
		m.headline = ""
		m.detail = ""
		return m, m.submit(name)
	}

	updated, cmd := m.name.Update(k)
	m.name = updated
	return m, cmd
}

// validateName mirrors the server's room-name rules (domain.RoomName): a
// trimmed, non-blank label of at most maxNameBytes bytes made of printable
// characters — spaces beyond ASCII (e.g. U+3000) allowed, control and
// invisible formatting characters rejected. Returns the error headline and
// ok=false when the name fails.
func validateName(name string) (string, bool) {
	switch {
	case name == "":
		return "Room name is required", false
	case len(name) > maxNameBytes:
		return "Room name is too long (max 64 bytes)", false
	case !utf8.ValidString(name):
		return "Room name contains invalid characters", false
	}
	for _, r := range name {
		if unicode.IsControl(r) || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			return "Room name contains invalid characters", false
		}
	}
	return "", true
}

// showError puts the modal back into editable mode with the given error rows.
// The typed name is preserved — the user fixes it rather than retypes it.
func (m Model) showError(headline, detail string) Model {
	m.creating = false
	m.headline = headline
	m.detail = detail
	m.name.Focus()
	return m
}
