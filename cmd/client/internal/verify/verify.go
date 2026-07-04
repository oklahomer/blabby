// Package verify defines the email-verification modal: a single 6-digit PIN
// field for the code mailed after registration (or surfaced by a pending-
// account login). It implements modal.Modal and reports its terminal outcomes
// as typed messages: the root Model returns to the login modal on
// api.VerifySucceeded (with a success notice) and on Cancelled — the
// transition itself stays in the root state machine.
package verify

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/modal"
	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// modalWidth is the width of the rendered modal box, in columns.
const modalWidth = 54

// validPIN mirrors the server's PIN rule: exactly six ASCII digits.
func validPIN(pin string) bool {
	if len(pin) != 6 {
		return false
	}
	for i := range pin {
		if !isASCIIDigit(pin[i]) {
			return false
		}
	}
	return true
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

// Submitter is the function the modal calls when the user presses enter with
// a well-formed PIN. It is the seam through which the root Model wires in
// api.VerifyEmailCmd.
type Submitter func(email, pin string) tea.Cmd

// Resender is the function the modal calls on ctrl+r to request a fresh PIN.
// The root Model wires in api.ResendVerificationCmd. Resends run without
// locking the field, so a code that is already arriving can still be typed.
type Resender func(email string) tea.Cmd

// Cancelled is the typed outcome emitted when the user presses esc: the root
// Model maps it back to the login modal. Email carries the address under
// verification so the login modal can prefill it.
type Cancelled struct {
	Email string
}

// Model is the verify modal state. It satisfies modal.Modal.
type Model struct {
	pin       textinput.Model
	email     string
	verifying bool
	headline  string
	detail    string
	notice    string
	server    string
	submit    Submitter
	resend    Resender
}

// New constructs a Model for the given registered email with the PIN field
// focused.
func New(submit Submitter, resend Resender, email, server string) Model {
	pin := textinput.New()
	pin.Placeholder = "6-digit code"
	pin.Prompt = ""
	pin.Width = modalWidth - 16
	pin.CharLimit = 6
	pin.Focus()

	return Model{
		pin:    pin,
		email:  email,
		server: server,
		submit: submit,
		resend: resend,
	}
}

// Init returns the textinput blink cmd so the PIN field shows a cursor.
// Implements modal.Modal.
func (m Model) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles key events and transport-outcome messages from the root
// Model. api.VerifySucceeded never reaches this modal — the root swaps back
// to the login modal on it. Implements modal.Modal.
func (m Model) Update(msg tea.Msg) (modal.Modal, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(v)
	case api.VerifyRejected:
		return m.showError(api.Humanise(v.Status, v.Message), ""), nil
	case api.VerifyTransportError:
		return m.showError("Cannot reach server at "+m.server, "("+v.Err.Error()+")"), nil
	case api.ResendSucceeded:
		m.notice = "A new code is on its way to " + m.email
		m.headline = ""
		m.detail = ""
		return m, nil
	case api.ResendRejected:
		m.headline = api.Humanise(v.Status, v.Message)
		m.detail = ""
		m.notice = ""
		return m, nil
	case api.ResendTransportError:
		m.headline = "Cannot reach server at " + m.server
		m.detail = "(" + v.Err.Error() + ")"
		m.notice = ""
		return m, nil
	}

	updated, cmd := m.pin.Update(msg)
	m.pin = updated
	return m, cmd
}

// View renders the modal box. Implements modal.Modal.
func (m Model) View(_, _ int) string {
	title := ui.Title().Render("Verify your email")

	body := []string{
		title,
		"",
		ui.Subtle().Render("We emailed a 6-digit code to"),
		ui.Label().Render(m.email),
		"",
		ui.Label().Render("Code:  ") + m.pin.View(),
	}

	if m.notice != "" {
		body = append(body, "", ui.Success().Render("✓ "+m.notice))
	}
	if m.headline != "" {
		body = append(body, "", ui.Error().Render("✗ "+m.headline))
		if m.detail != "" {
			body = append(body, ui.Subtle().Render("  "+m.detail))
		}
	}

	body = append(body, "")
	if m.verifying {
		body = append(body, ui.Subtle().Render("Verifying…"))
	} else {
		body = append(body, ui.Subtle().Render("enter: verify · ctrl+r: resend code · esc: back to sign in"))
	}

	return ui.ModalBorder().Width(modalWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, body...),
	)
}

// handleKey routes a key event through the submit / resend / esc / in-flight
// rules in priority order. Single non-digit characters are absorbed so the
// field only ever holds PIN-shaped input.
func (m Model) handleKey(k tea.KeyMsg) (modal.Modal, tea.Cmd) {
	if m.verifying {
		// ctrl+c is handled at the root level; every other key is suppressed
		// while we wait for the server.
		return m, nil
	}

	switch k.String() {
	case "ctrl+1", "ctrl+2", "ctrl+3":
		return m, nil
	case "esc":
		return m, func() tea.Msg { return Cancelled{Email: m.email} }
	case "ctrl+r":
		return m, m.resend(m.email)
	case "enter":
		pin := strings.TrimSpace(m.pin.Value())
		if !validPIN(pin) {
			m.headline = "Enter the 6-digit code from the email"
			m.detail = ""
			m.notice = ""
			return m, nil
		}
		m.verifying = true
		m.headline = ""
		m.detail = ""
		m.notice = ""
		return m, m.submit(m.email, pin)
	}

	for _, r := range k.Runes {
		if r < '0' || r > '9' {
			return m, nil
		}
	}
	updated, cmd := m.pin.Update(k)
	m.pin = updated
	return m, cmd
}

// showError puts the modal back into editable mode with the given error rows;
// the PIN field is cleared and refocused so the user can retype directly.
func (m Model) showError(headline, detail string) Model {
	m.verifying = false
	m.headline = headline
	m.detail = detail
	m.notice = ""
	m.pin.SetValue("")
	m.pin.Focus()
	return m
}
