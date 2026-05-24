// Package login defines the centred login modal that is the first
// view a TUI user interacts with. It implements modal.Modal, owns
// the two textinput fields, and is the one place in the client
// where esc-dismissal collapses into tea.Quit (because there is no
// prior chrome state to return to before authentication).
package login

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/modal"
	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

const (
	// modalWidth is the width of the rendered login modal box, in
	// columns. Wider than the typical chrome inner pane so labels +
	// inputs sit comfortably on one row each.
	modalWidth = 50

	// maxUsernameBytes mirrors the server's username byte cap
	// (internal/gateway/handler.go maxUsernameBytes). The textinput's
	// CharLimit counts runes, not bytes, so we re-check by byte length
	// at submit to keep multi-byte usernames from being rejected only
	// after the HTTP round-trip.
	maxUsernameBytes = 64
	maxPasswordBytes = 256

	usernameSlot = 0
	passwordSlot = 1
)

// phase enumerates the in-flight indicator copy. phaseIdle means
// the modal is editable; phaseSigningIn shows "Signing in…" during
// the HTTP login; phaseConnecting shows "Connecting…" during the
// subsequent WebSocket handshake.
type phase int

const (
	phaseIdle phase = iota
	phaseSigningIn
	phaseConnecting
)

// Submitter is the function the modal calls when the user presses
// enter with both fields populated. It is the seam through which the
// root Model wires in api.LoginCmd; injecting it via a function
// pointer (rather than reaching into api directly) keeps the login
// package free of HTTP concerns and trivially unit-testable.
type Submitter func(username, password string) tea.Cmd

// Model is the login modal state. It satisfies modal.Modal.
type Model struct {
	inputs   [2]textinput.Model
	focused  int
	phase    phase
	headline string
	detail   string
	server   string
	submit   Submitter
}

// New constructs a Model with the username field focused and the
// password field masked. submit is invoked when the user presses
// enter with both fields non-empty. server is rendered into the
// "Cannot reach server at {server}" headline (AC #12) so the user
// sees which endpoint the client could not reach.
func New(submit Submitter, server string) Model {
	username := textinput.New()
	username.Placeholder = "username"
	username.Prompt = ""
	username.Width = modalWidth - 14
	// CharLimit is a rune count; the server enforces a byte cap of
	// maxUsernameBytes. We allow the user to type up to that many
	// runes here (so single-byte usernames feel natural) and re-check
	// by bytes at submit.
	username.CharLimit = maxUsernameBytes
	username.Focus()

	password := textinput.New()
	password.Placeholder = "password"
	password.Prompt = ""
	password.Width = modalWidth - 14
	password.CharLimit = maxPasswordBytes
	password.EchoMode = textinput.EchoPassword
	password.EchoCharacter = '*'

	return Model{
		inputs:  [2]textinput.Model{username, password},
		focused: usernameSlot,
		server:  server,
		submit:  submit,
	}
}

// Init returns the textinput blink cmd so the focused field shows a
// cursor. Implements modal.Modal.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles key events and transport-outcome messages from the
// root Model. It returns (nil, tea.Quit) when the user presses esc
// while not in flight — the login modal is the bottom of the
// dismissal stack, so dismissing it quits the program.
// Implements modal.Modal.
func (m Model) Update(msg tea.Msg) (modal.Modal, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case api.LoginRejected:
		return m.handleRejection(api.Humanise(v.Status, v.Message), ""), nil
	case api.LoginTransportError:
		return m.handleRejection("Cannot reach server at "+m.server, "("+v.Err.Error()+")"), nil
	case api.WSAuthRejected:
		return m.handleRejection(api.Humanise(v.Status, v.Message), ""), nil
	case api.WSDialFailed:
		return m.handleRejection("Signed in, but WebSocket handshake failed", "("+v.Err.Error()+")"), nil
	case api.WSAuthTimedOut:
		return m.handleRejection("Authentication timed out", ""), nil
	}

	updated, cmd := m.inputs[m.focused].Update(msg)
	m.inputs[m.focused] = updated
	return m, cmd
}

// View renders the modal box. width and height are the full screen
// dimensions, used so the modal can size itself proportionally; the
// chrome supplies the centring via modal.Overlay. Implements
// modal.Modal.
func (m Model) View(_, _ int) string {
	title := ui.Title().Render("Sign in to blabby")

	body := []string{
		title,
		"",
		ui.Label().Render("Username:  ") + m.inputs[usernameSlot].View(),
		ui.Label().Render("Password:  ") + m.inputs[passwordSlot].View(),
	}

	if m.headline != "" {
		body = append(body, "", ui.Error().Render("✗ "+m.headline))
		if m.detail != "" {
			body = append(body, ui.Subtle().Render("  "+m.detail))
		}
	}

	body = append(body, "")
	switch m.phase {
	case phaseSigningIn:
		body = append(body, ui.Subtle().Render("Signing in…"))
	case phaseConnecting:
		body = append(body, ui.Subtle().Render("Connecting…"))
	default:
		body = append(body, ui.Subtle().Render("tab: next field · enter: submit · esc: quit"))
	}

	return ui.ModalBorder().Width(modalWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, body...),
	)
}

// SetConnecting switches the in-flight indicator copy from "Signing
// in…" to "Connecting…" once the HTTP login has succeeded and the
// WebSocket dial is in progress. The root Model calls this when it
// dispatches DialAndAuthCmd.
func (m Model) SetConnecting() Model {
	m.headline = ""
	m.detail = ""
	m.phase = phaseConnecting
	return m
}

// ShowError puts the modal back into editable mode with the given
// headline and detail rows. Use this for entry points that bypass
// the api.* transport messages (e.g., re-opening the modal after a
// WebSocket disconnect with a custom "Connection lost" headline).
// The username field is always preserved; the password is cleared
// and refocused so the user can retype it directly.
func (m Model) ShowError(headline, detail string) Model {
	m.phase = phaseIdle
	m.headline = headline
	m.detail = detail
	m.inputs[passwordSlot].SetValue("")
	m.inputs[usernameSlot].Blur()
	m.inputs[passwordSlot].Focus()
	m.focused = passwordSlot
	return m
}

// PrefillUsername populates the username field. Used when re-opening
// the modal after a session drop so the user does not have to retype
// the username they were just signed in as.
func (m Model) PrefillUsername(username string) Model {
	m.inputs[usernameSlot].SetValue(username)
	return m
}

// handleKey routes a key event through the focus / submit / esc /
// in-flight rules in priority order. The function returns a
// modal.Modal because Update's signature requires it; the concrete
// returned value is always either *m, or nil for the esc-quit case.
func (m Model) handleKey(k tea.KeyMsg) (modal.Modal, tea.Cmd) {
	if m.inFlight() {
		// ctrl+c is handled at the root level; every other key is
		// suppressed while we wait for the server.
		return m, nil
	}

	switch k.String() {
	case "ctrl+1", "ctrl+2", "ctrl+3":
		// Pane-focus shortcuts have a global meaning when no modal
		// is open; while the modal is open they must be absorbed
		// silently so they neither switch background pane focus
		// nor leak into the textinput.
		return m, nil
	case "esc":
		return nil, tea.Quit
	case "tab", "down":
		return m.shiftFocus(1), nil
	case "shift+tab", "up":
		return m.shiftFocus(-1), nil
	case "enter":
		username := strings.TrimSpace(m.inputs[usernameSlot].Value())
		password := m.inputs[passwordSlot].Value()
		if username == "" || password == "" {
			m.headline = "Username and password are required"
			m.detail = ""
			return m, nil
		}
		// Server enforces byte limits; re-check here so multi-byte
		// runes surface a friendlier error than the round-tripped
		// "Invalid request".
		if len(username) > maxUsernameBytes || len(password) > maxPasswordBytes {
			m.headline = "Username or password is too long"
			m.detail = ""
			return m, nil
		}
		m.phase = phaseSigningIn
		m.headline = ""
		m.detail = ""
		return m, m.submit(username, password)
	}

	updated, cmd := m.inputs[m.focused].Update(k)
	m.inputs[m.focused] = updated
	return m, cmd
}

// inFlight reports whether the modal is currently awaiting a server
// response. Keys other than ctrl+c are absorbed in this state.
func (m Model) inFlight() bool {
	return m.phase == phaseSigningIn || m.phase == phaseConnecting
}

// shiftFocus moves focus forward (delta=+1) or backward (-1)
// between the two input fields. Returns the updated Model.
func (m Model) shiftFocus(delta int) Model {
	m.inputs[m.focused].Blur()
	m.focused = (m.focused + delta + len(m.inputs)) % len(m.inputs)
	m.inputs[m.focused].Focus()
	return m
}

// handleRejection puts the modal back into editable mode with the
// supplied error text, clears the password field, and focuses
// password so the user can retype. Keeps whatever the user had
// typed in the username field — they just had a bad password or a
// transient transport error and shouldn't have to retype the user.
func (m Model) handleRejection(headline, detail string) Model {
	return m.ShowError(headline, detail)
}
