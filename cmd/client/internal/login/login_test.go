package login

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// noopSubmitter records the most recent (email, password) it was
// called with and returns a sentinel Cmd. Used to assert that
// enter-submit fires for valid input and is suppressed otherwise.
type recorder struct {
	called   bool
	email    string
	password string
}

func (r *recorder) submit(u, p string) tea.Cmd {
	r.called = true
	r.email = u
	r.password = p
	return func() tea.Msg { return "sentinel" }
}

// keyMsg builds a tea.KeyMsg whose String() matches s, for the small
// vocabulary the login modal cares about. Falls back to a runes-only
// key for single printable characters.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// typeIn feeds each rune of s into the model as a separate KeyMsg,
// returning the resulting Model. Matches how Bubble Tea delivers
// real keystrokes to textinput.Model.
func typeIn(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		cast, ok := next.(Model)
		if !ok {
			t.Fatalf("expected login.Model, got %T", next)
		}
		m = cast
	}
	return m
}

func TestEnterSubmitsWithBothFieldsPopulated(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	m := New(rec.submit, "http://localhost:8080")
	m = typeIn(t, m, "rina@example.com")
	m = stepTab(t, m)
	m = typeIn(t, m, "hunter2")
	next, cmd := m.Update(keyMsg("enter"))

	if cmd == nil {
		t.Fatal("expected submit Cmd, got nil")
	}
	cmd() // force submit invocation
	if !rec.called {
		t.Fatal("submit not invoked")
	}
	if rec.email != "rina@example.com" || rec.password != "hunter2" {
		t.Fatalf("got %q/%q", rec.email, rec.password)
	}
	if got, ok := next.(Model); !ok || !got.inFlight() {
		t.Fatal("expected inFlight after enter")
	}
}

func TestEnterWithEmptyFieldShowsInlineError(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	m = typeIn(t, m, "rina@example.com")
	next, cmd := m.Update(keyMsg("enter")) // password still empty

	if cmd != nil {
		t.Fatal("expected no submit when fields empty")
	}
	got, ok := next.(Model)
	if !ok {
		t.Fatal("expected Model")
	}
	if !strings.Contains(got.headline, "required") {
		t.Fatalf("expected required-field message, got %q", got.headline)
	}
	if got.inFlight() {
		t.Fatal("should not have entered in-flight on empty submit")
	}
}

func TestEscQuitsWhenNotInFlight(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	next, cmd := m.Update(keyMsg("esc"))
	if next != nil {
		t.Fatalf("expected nil modal (dismissed), got %T", next)
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd")
	}
}

func TestKeysSuppressedWhileInFlight(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	m.phase = phaseSigningIn

	next, cmd := m.Update(keyMsg("esc"))
	if cmd != nil {
		t.Fatal("esc must not quit while in-flight")
	}
	if _, ok := next.(Model); !ok {
		t.Fatal("expected Model returned")
	}

	// Typing should also be absorbed.
	next, _ = m.Update(keyMsg("x"))
	if _, ok := next.(Model); !ok {
		t.Fatal("expected Model returned")
	}
}

func TestRejectionClearsPasswordAndShowsHeadline(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	m = typeIn(t, m, "rina@example.com")
	m = stepTab(t, m)
	m = typeIn(t, m, "wrong")
	m.phase = phaseSigningIn

	rejection := api.LoginRejected{Status: "AUTH_INVALID_TOKEN", Message: "invalid credentials"}
	next, _ := m.Update(rejection)
	got, ok := next.(Model)
	if !ok {
		t.Fatal("expected Model")
	}
	if got.inFlight() {
		t.Fatal("expected inFlight=false after rejection")
	}
	if got.inputs[passwordSlot].Value() != "" {
		t.Fatalf("expected password cleared, got %q", got.inputs[passwordSlot].Value())
	}
	if got.focused != passwordSlot {
		t.Fatalf("expected password focused, got %d", got.focused)
	}
	if !strings.Contains(got.headline, "Invalid credentials") {
		t.Fatalf("expected humanised headline, got %q", got.headline)
	}
}

func TestTransportErrorIncludesUnderlyingReason(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	m.phase = phaseSigningIn

	next, _ := m.Update(api.LoginTransportError{Err: errors.New("connection refused")})
	got, ok := next.(Model)
	if !ok {
		t.Fatal("expected Model")
	}
	if !strings.Contains(got.headline, "Cannot reach server") {
		t.Fatalf("missing headline: %q", got.headline)
	}
	if !strings.Contains(got.detail, "connection refused") {
		t.Fatalf("missing detail: %q", got.detail)
	}
}

func TestWSDialFailedHeadlineMentionsHandshake(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	m.phase = phaseSigningIn

	next, _ := m.Update(api.WSDialFailed{Err: errors.New("upgrade refused")})
	got, ok := next.(Model)
	if !ok {
		t.Fatal("expected Model")
	}
	if !strings.Contains(got.headline, "WebSocket handshake failed") {
		t.Fatalf("missing handshake headline: %q", got.headline)
	}
	if !strings.Contains(got.detail, "upgrade refused") {
		t.Fatalf("missing detail: %q", got.detail)
	}
}

func TestSetConnectingFlipsInFlightCopy(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080").SetConnecting()
	if !m.inFlight() {
		t.Fatal("SetConnecting must set inFlight")
	}
	view := m.View(80, 24)
	if !strings.Contains(view, "Connecting…") {
		t.Fatalf("expected Connecting… text, got %s", view)
	}
	if strings.Contains(view, "Signing in") {
		t.Fatalf("Signing in… should have been replaced by Connecting…: %s", view)
	}
}

func TestFocusCyclesViaTab(t *testing.T) {
	t.Parallel()
	m := New((&recorder{}).submit, "http://localhost:8080")
	if m.focused != emailSlot {
		t.Fatalf("initial focus = %d, want email", m.focused)
	}
	m = stepTab(t, m)
	if m.focused != passwordSlot {
		t.Fatalf("after tab focus = %d, want password", m.focused)
	}
	m = stepShiftTab(t, m)
	if m.focused != emailSlot {
		t.Fatalf("after shift+tab focus = %d, want email", m.focused)
	}
}

// stepTab feeds a tab key and asserts the resulting modal is still
// a login.Model. Encapsulates the (modal.Modal, tea.Cmd) → Model
// unwrap so the call sites stay one-line.
func stepTab(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(keyMsg("tab"))
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("expected login.Model, got %T", next)
	}
	return got
}

// stepShiftTab is the inverse of stepTab.
func stepShiftTab(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(keyMsg("shift+tab"))
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("expected login.Model, got %T", next)
	}
	return got
}
