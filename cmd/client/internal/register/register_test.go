package register

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// recorder records the most recent submission and returns a sentinel Cmd.
type recorder struct {
	called   bool
	email    string
	handle   string
	password string
}

func (r *recorder) submit(email, handle, password string) tea.Cmd {
	r.called = true
	r.email = email
	r.handle = handle
	r.password = password
	return func() tea.Msg { return "sentinel" }
}

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
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// typeIn feeds each rune of s into the model as a separate KeyMsg.
func typeIn(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		cast, ok := next.(Model)
		if !ok {
			t.Fatalf("expected register.Model, got %T", next)
		}
		m = cast
	}
	return m
}

func stepKey(t *testing.T, m Model, key string) Model {
	t.Helper()
	next, _ := m.Update(keyMsg(key))
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("expected register.Model, got %T", next)
	}
	return got
}

// filled returns a model with all four fields validly populated.
func filled(t *testing.T, rec *recorder) Model {
	t.Helper()
	m := New(rec.submit, "http://localhost:8080")
	m = typeIn(t, m, "dana@example.com")
	m = stepKey(t, m, "tab")
	m = typeIn(t, m, "Dana_99")
	m = stepKey(t, m, "tab")
	m = typeIn(t, m, "a-long-passphrase")
	m = stepKey(t, m, "tab")
	m = typeIn(t, m, "a-long-passphrase")
	return m
}

func TestEnterSubmitsValidForm(t *testing.T) {
	rec := &recorder{}
	m := filled(t, rec)

	next, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("expected submit Cmd, got nil")
	}
	cmd()
	if !rec.called {
		t.Fatal("submit not invoked")
	}
	if rec.email != "dana@example.com" || rec.handle != "Dana_99" || rec.password != "a-long-passphrase" {
		t.Fatalf("got %q/%q/%q", rec.email, rec.handle, rec.password)
	}
	if got := next.(Model); !got.creating {
		t.Fatal("expected in-flight after enter")
	}
}

func TestSubmitValidationErrors(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*testing.T, Model) Model
		headline string
	}{
		{
			name: "missing fields",
			mutate: func(t *testing.T, m Model) Model {
				return New((&recorder{}).submit, "srv")
			},
			headline: "required",
		},
		{
			name: "bad handle",
			mutate: func(t *testing.T, m Model) Model {
				fresh := New((&recorder{}).submit, "srv")
				fresh = typeIn(t, fresh, "dana@example.com")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "d!")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "a-long-passphrase")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "a-long-passphrase")
				return fresh
			},
			headline: "Handle must be",
		},
		{
			name: "short password",
			mutate: func(t *testing.T, m Model) Model {
				fresh := New((&recorder{}).submit, "srv")
				fresh = typeIn(t, fresh, "dana@example.com")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "Dana_99")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "short")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "short")
				return fresh
			},
			headline: "at least 12",
		},
		{
			name: "password mismatch",
			mutate: func(t *testing.T, m Model) Model {
				fresh := New((&recorder{}).submit, "srv")
				fresh = typeIn(t, fresh, "dana@example.com")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "Dana_99")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "a-long-passphrase")
				fresh = stepKey(t, fresh, "tab")
				fresh = typeIn(t, fresh, "different-passphrase")
				return fresh
			},
			headline: "do not match",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.mutate(t, Model{})
			next, cmd := m.Update(keyMsg("enter"))
			if cmd != nil {
				t.Fatal("expected no submit Cmd for invalid form")
			}
			got := next.(Model)
			if !strings.Contains(got.headline, tc.headline) {
				t.Fatalf("headline = %q, want it to contain %q", got.headline, tc.headline)
			}
			if got.creating {
				t.Fatal("must not go in-flight on invalid form")
			}
		})
	}
}

func TestEscEmitsCancelledWithTypedEmail(t *testing.T) {
	m := New((&recorder{}).submit, "srv")
	m = typeIn(t, m, "dana@example.com")
	next, cmd := m.Update(keyMsg("esc"))
	if _, ok := next.(Model); !ok {
		t.Fatalf("expected the modal to stay until the root swaps it, got %T", next)
	}
	if cmd == nil {
		t.Fatal("expected a Cancelled cmd")
	}
	got, ok := cmd().(Cancelled)
	if !ok {
		t.Fatal("expected Cancelled message")
	}
	if got.Email != "dana@example.com" {
		t.Fatalf("Cancelled.Email = %q, want the typed email", got.Email)
	}
}

func TestKeysSuppressedWhileCreating(t *testing.T) {
	rec := &recorder{}
	m := filled(t, rec)
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, cmd := m.Update(keyMsg("esc"))
	if cmd != nil {
		t.Fatal("esc must be absorbed while in-flight")
	}
	if _, ok := next.(Model); !ok {
		t.Fatal("expected Model returned")
	}
}

func TestRejectionClearsPasswordsAndShowsHeadline(t *testing.T) {
	rec := &recorder{}
	m := filled(t, rec)
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, _ = m.Update(api.RegisterRejected{Status: "HANDLE_ALREADY_TAKEN", Message: "handle is already taken"})
	got := next.(Model)
	if got.creating {
		t.Fatal("expected editable after rejection")
	}
	if got.inputs[passwordSlot].Value() != "" || got.inputs[confirmSlot].Value() != "" {
		t.Fatal("expected both password fields cleared")
	}
	if !strings.Contains(got.headline, "Handle already taken") {
		t.Fatalf("expected humanised headline, got %q", got.headline)
	}
}

func TestTransportErrorIncludesUnderlyingReason(t *testing.T) {
	rec := &recorder{}
	m := filled(t, rec)
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, _ = m.Update(api.RegisterTransportError{Err: errors.New("connection refused")})
	got := next.(Model)
	if !strings.Contains(got.headline, "Cannot reach server") {
		t.Fatalf("missing headline: %q", got.headline)
	}
	if !strings.Contains(got.detail, "connection refused") {
		t.Fatalf("missing detail: %q", got.detail)
	}
}

func TestViewShowsHelpAndInFlightCopy(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "srv")
	if view := m.View(80, 24); !strings.Contains(view, "create account") {
		t.Fatalf("help line missing: %s", view)
	}
	m = filled(t, rec)
	next, _ := m.Update(keyMsg("enter"))
	if view := next.(Model).View(80, 24); !strings.Contains(view, "Creating account…") {
		t.Fatalf("in-flight copy missing: %s", view)
	}
}
