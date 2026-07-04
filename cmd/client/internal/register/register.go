// Package register defines the account-creation modal reached from the login
// modal via ctrl+n. It implements modal.Modal, owns the four textinput fields,
// and reports its terminal outcomes as typed messages: the root Model swaps to
// the verify modal on api.RegisterSucceeded and back to the login modal on
// Cancelled — the transition itself stays in the root state machine.
package register

import (
	"regexp"
	"strings"

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

	// The byte caps mirror the server's registration rules
	// (internal/gateway/registration.go); re-checking at submit surfaces a
	// friendlier error than the round-tripped envelope.
	maxMailAddressBytes = 254
	minPasswordBytes    = 12
	maxPasswordBytes    = 256

	emailSlot    = 0
	handleSlot   = 1
	passwordSlot = 2
	confirmSlot  = 3
)

// handlePattern mirrors the server's handle rule: 3–30 characters of letters,
// digits, or underscore.
var handlePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,30}$`)

// Submitter is the function the modal calls when the user presses enter with a
// valid form. It is the seam through which the root Model wires in
// api.RegisterCmd, keeping this package free of HTTP concerns.
type Submitter func(email, handle, password string) tea.Cmd

// Cancelled is the typed outcome emitted when the user presses esc: the root
// Model maps it back to the login modal. Email carries whatever address was
// typed so the login modal can prefill it — the email is not a secret, and
// every other login-reopen path preserves it too.
type Cancelled struct {
	Email string
}

// Model is the register modal state. It satisfies modal.Modal.
type Model struct {
	inputs   [4]textinput.Model
	focused  int
	creating bool
	headline string
	detail   string
	server   string
	submit   Submitter
}

// New constructs a Model with the email field focused and both password
// fields masked. server is rendered into the "Cannot reach server at
// {server}" headline on transport errors.
func New(submit Submitter, server string) Model {
	newField := func(placeholder string, limit int, masked bool) textinput.Model {
		in := textinput.New()
		in.Placeholder = placeholder
		in.Prompt = ""
		in.Width = modalWidth - 16
		in.CharLimit = limit
		if masked {
			in.EchoMode = textinput.EchoPassword
			in.EchoCharacter = '*'
		}
		return in
	}

	email := newField("email", maxMailAddressBytes, false)
	email.Focus()

	return Model{
		inputs: [4]textinput.Model{
			email,
			newField("3-30 letters, digits, _", 30, false),
			newField("12+ characters", maxPasswordBytes, true),
			newField("repeat password", maxPasswordBytes, true),
		},
		focused: emailSlot,
		server:  server,
		submit:  submit,
	}
}

// Init returns the textinput blink cmd so the focused field shows a cursor.
// Implements modal.Modal.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles key events and transport-outcome messages from the root
// Model. api.RegisterSucceeded never reaches this modal — the root swaps to
// the verify modal on it. Implements modal.Modal.
func (m Model) Update(msg tea.Msg) (modal.Modal, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case api.RegisterRejected:
		return m.showError(api.Humanise(v.Status, v.Message), ""), nil
	case api.RegisterTransportError:
		return m.showError("Cannot reach server at "+m.server, "("+v.Err.Error()+")"), nil
	}

	updated, cmd := m.inputs[m.focused].Update(msg)
	m.inputs[m.focused] = updated
	return m, cmd
}

// View renders the modal box. Implements modal.Modal.
func (m Model) View(_, _ int) string {
	title := ui.Title().Render("Create your blabby account")

	body := []string{
		title,
		"",
		ui.Label().Render("Email:     ") + m.inputs[emailSlot].View(),
		ui.Label().Render("Handle:    ") + m.inputs[handleSlot].View(),
		ui.Label().Render("Password:  ") + m.inputs[passwordSlot].View(),
		ui.Label().Render("Confirm:   ") + m.inputs[confirmSlot].View(),
	}

	if m.headline != "" {
		body = append(body, "", ui.Error().Render("✗ "+m.headline))
		if m.detail != "" {
			body = append(body, ui.Subtle().Render("  "+m.detail))
		}
	}

	body = append(body, "")
	if m.creating {
		body = append(body, ui.Subtle().Render("Creating account…"))
	} else {
		body = append(body, ui.Subtle().Render("tab: next field · enter: create account · esc: back to sign in"))
	}

	return ui.ModalBorder().Width(modalWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, body...),
	)
}

// handleKey routes a key event through the focus / submit / esc / in-flight
// rules in priority order.
func (m Model) handleKey(k tea.KeyMsg) (modal.Modal, tea.Cmd) {
	if m.creating {
		// ctrl+c is handled at the root level; every other key is suppressed
		// while we wait for the server.
		return m, nil
	}

	switch k.String() {
	case "ctrl+1", "ctrl+2", "ctrl+3":
		// Absorbed for the same reason as in the login modal: pane-focus
		// shortcuts must not leak behind an open modal.
		return m, nil
	case "esc":
		email := strings.TrimSpace(m.inputs[emailSlot].Value())
		return m, func() tea.Msg { return Cancelled{Email: email} }
	case "tab", "down":
		return m.shiftFocus(1), nil
	case "shift+tab", "up":
		return m.shiftFocus(-1), nil
	case "enter":
		return m.submitForm()
	}

	updated, cmd := m.inputs[m.focused].Update(k)
	m.inputs[m.focused] = updated
	return m, cmd
}

// submitForm validates the four fields against the server's rules and, when
// they pass, dispatches the Submitter with the modal locked in-flight.
func (m Model) submitForm() (modal.Modal, tea.Cmd) {
	email := strings.TrimSpace(m.inputs[emailSlot].Value())
	handle := strings.TrimSpace(m.inputs[handleSlot].Value())
	password := m.inputs[passwordSlot].Value()
	confirm := m.inputs[confirmSlot].Value()

	switch {
	case email == "" || handle == "" || password == "" || confirm == "":
		m.headline = "All fields are required"
	case len(email) > maxMailAddressBytes:
		m.headline = "Email is too long"
	case !handlePattern.MatchString(handle):
		m.headline = "Handle must be 3-30 letters, digits, or _"
	case len(password) < minPasswordBytes:
		m.headline = "Password must be at least 12 characters"
	case len(password) > maxPasswordBytes:
		m.headline = "Password is too long"
	case password != confirm:
		m.headline = "Passwords do not match"
	default:
		m.creating = true
		m.headline = ""
		m.detail = ""
		return m, m.submit(email, handle, password)
	}
	m.detail = ""
	return m, nil
}

// showError puts the modal back into editable mode with the given error rows.
// Both password fields are cleared and the focus rules mirror the login
// modal's rejection handling: the user re-enters the secret, not the identity.
func (m Model) showError(headline, detail string) Model {
	m.creating = false
	m.headline = headline
	m.detail = detail
	m.inputs[passwordSlot].SetValue("")
	m.inputs[confirmSlot].SetValue("")
	m.inputs[m.focused].Blur()
	m.focused = passwordSlot
	m.inputs[passwordSlot].Focus()
	return m
}

// shiftFocus moves focus forward (delta=+1) or backward (-1) between the four
// input fields.
func (m Model) shiftFocus(delta int) Model {
	m.inputs[m.focused].Blur()
	m.focused = (m.focused + delta + len(m.inputs)) % len(m.inputs)
	m.inputs[m.focused].Focus()
	return m
}
