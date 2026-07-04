package verify

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// recorder records the most recent submit/resend and returns sentinel Cmds.
type recorder struct {
	submitCalled bool
	resendCalled bool
	email        string
	pin          string
}

func (r *recorder) submit(email, pin string) tea.Cmd {
	r.submitCalled = true
	r.email = email
	r.pin = pin
	return func() tea.Msg { return "sentinel" }
}

func (r *recorder) resend(email string) tea.Cmd {
	r.resendCalled = true
	r.email = email
	return func() tea.Msg { return "sentinel" }
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+r":
		return tea.KeyMsg{Type: tea.KeyCtrlR}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeIn(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		cast, ok := next.(Model)
		if !ok {
			t.Fatalf("expected verify.Model, got %T", next)
		}
		m = cast
	}
	return m
}

func TestEnterSubmitsSixDigitPIN(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	m = typeIn(t, m, "482910")

	next, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("expected submit Cmd, got nil")
	}
	cmd()
	if !rec.submitCalled || rec.email != "dana@example.com" || rec.pin != "482910" {
		t.Fatalf("submit = %v email=%q pin=%q", rec.submitCalled, rec.email, rec.pin)
	}
	if got := next.(Model); !got.verifying {
		t.Fatal("expected in-flight after enter")
	}
}

func TestEnterRejectsMalformedPIN(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	m = typeIn(t, m, "123")

	next, cmd := m.Update(keyMsg("enter"))
	if cmd != nil {
		t.Fatal("expected no submit for a short PIN")
	}
	got := next.(Model)
	if !strings.Contains(got.headline, "6-digit") {
		t.Fatalf("headline = %q", got.headline)
	}
	if got.verifying {
		t.Fatal("must not go in-flight on invalid PIN")
	}
}

func TestNonDigitInputIsAbsorbed(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	m = typeIn(t, m, "12ab34x")
	if got := m.pin.Value(); got != "1234" {
		t.Fatalf("pin value = %q, want digits only", got)
	}
}

func TestCtrlRDispatchesResendWithoutLocking(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")

	next, cmd := m.Update(keyMsg("ctrl+r"))
	if cmd == nil {
		t.Fatal("expected resend Cmd")
	}
	cmd()
	if !rec.resendCalled || rec.email != "dana@example.com" {
		t.Fatalf("resend = %v email=%q", rec.resendCalled, rec.email)
	}
	got := next.(Model)
	if got.verifying {
		t.Fatal("resend must not lock the field")
	}
	// Typing still works while the resend is in flight.
	got = typeIn(t, got, "4")
	if got.pin.Value() != "4" {
		t.Fatalf("pin = %q, want typed digit", got.pin.Value())
	}
}

func TestResendOutcomesRender(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")

	next, _ := m.Update(api.ResendSucceeded{})
	got := next.(Model)
	if !strings.Contains(got.notice, "new code is on its way") {
		t.Fatalf("notice = %q", got.notice)
	}

	next, _ = got.Update(api.ResendRejected{Status: "VERIFICATION_RATE_LIMITED", Message: "wait"})
	got = next.(Model)
	if got.notice != "" {
		t.Fatal("notice must clear on rejection")
	}
	if !strings.Contains(got.headline, "Too many verification attempts") {
		t.Fatalf("headline = %q", got.headline)
	}
}

func TestVerifyRejectionClearsPIN(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	m = typeIn(t, m, "000000")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, _ = m.Update(api.VerifyRejected{Status: "VERIFICATION_INVALID", Message: "verification failed"})
	got := next.(Model)
	if got.verifying {
		t.Fatal("expected editable after rejection")
	}
	if got.pin.Value() != "" {
		t.Fatalf("expected PIN cleared, got %q", got.pin.Value())
	}
	if !strings.Contains(got.headline, "invalid or expired") {
		t.Fatalf("headline = %q", got.headline)
	}
}

func TestTransportErrorIncludesUnderlyingReason(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	m = typeIn(t, m, "482910")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, _ = m.Update(api.VerifyTransportError{Err: errors.New("connection refused")})
	got := next.(Model)
	if !strings.Contains(got.headline, "Cannot reach server") {
		t.Fatalf("headline = %q", got.headline)
	}
	if !strings.Contains(got.detail, "connection refused") {
		t.Fatalf("detail = %q", got.detail)
	}
}

func TestEscEmitsCancelled(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	next, cmd := m.Update(keyMsg("esc"))
	if _, ok := next.(Model); !ok {
		t.Fatalf("expected the modal to stay until the root swaps it, got %T", next)
	}
	if cmd == nil {
		t.Fatal("expected a Cancelled cmd")
	}
	if _, ok := cmd().(Cancelled); !ok {
		t.Fatal("expected Cancelled message")
	}
}

func TestKeysSuppressedWhileVerifying(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	m = typeIn(t, m, "482910")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	if _, cmd := m.Update(keyMsg("esc")); cmd != nil {
		t.Fatal("esc must be absorbed while verifying")
	}
	if _, cmd := m.Update(keyMsg("ctrl+r")); cmd != nil {
		t.Fatal("ctrl+r must be absorbed while verifying")
	}
}

func TestViewShowsEmailAndHelp(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, rec.resend, "dana@example.com", "srv")
	view := m.View(80, 24)
	if !strings.Contains(view, "dana@example.com") {
		t.Fatalf("email missing from view: %s", view)
	}
	if !strings.Contains(view, "resend code") {
		t.Fatalf("help line missing: %s", view)
	}
}
