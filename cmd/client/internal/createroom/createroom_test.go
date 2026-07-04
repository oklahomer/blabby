package createroom

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// recorder records the most recent submission and returns a sentinel Cmd.
type recorder struct {
	called bool
	name   string
}

func (r *recorder) submit(name string) tea.Cmd {
	r.called = true
	r.name = name
	return func() tea.Msg { return "sentinel" }
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeIn(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		cast, ok := next.(Model)
		if !ok {
			t.Fatalf("expected createroom.Model, got %T", next)
		}
		m = cast
	}
	return m
}

func TestEnterSubmitsTrimmedName(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "srv")
	m = typeIn(t, m, " Team Standup ")

	next, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("expected submit Cmd, got nil")
	}
	cmd()
	if !rec.called || rec.name != "Team Standup" {
		t.Fatalf("submit = %v name=%q", rec.called, rec.name)
	}
	if got := next.(Model); !got.creating {
		t.Fatal("expected in-flight after enter")
	}
}

func TestValidateNameRejections(t *testing.T) {
	cases := map[string]struct {
		name     string
		headline string
	}{
		"empty":            {"", "required"},
		"blank":            {"   ", "required"},
		"over max bytes":   {strings.Repeat("字", 22), "too long"},
		"zero-width space": {"sneaky\u200bname", "invalid characters"},
	}
	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			rec := &recorder{}
			m := New(rec.submit, "srv")
			m.name.SetValue(tc.name)
			next, cmd := m.Update(keyMsg("enter"))
			if cmd != nil {
				t.Fatal("expected no submit for an invalid name")
			}
			got := next.(Model)
			if !strings.Contains(got.headline, tc.headline) {
				t.Fatalf("headline = %q, want it to contain %q", got.headline, tc.headline)
			}
		})
	}
}

func TestCJKNameWithIdeographicSpaceIsValid(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "srv")
	m.name.SetValue("雑談　部屋")
	_, cmd := m.Update(keyMsg("enter"))
	if cmd == nil {
		t.Fatal("expected submit Cmd for a valid CJK name with U+3000")
	}
	cmd()
	if rec.name != "雑談　部屋" {
		t.Fatalf("name = %q", rec.name)
	}
}

func TestEscEmitsCancelled(t *testing.T) {
	m := New((&recorder{}).submit, "srv")
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

func TestKeysSuppressedWhileCreating(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "srv")
	m = typeIn(t, m, "Team Standup")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	if _, cmd := m.Update(keyMsg("esc")); cmd != nil {
		t.Fatal("esc must be absorbed while in-flight")
	}
}

func TestCreateFailedRendersAndKeepsName(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "srv")
	m = typeIn(t, m, "Team Standup")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, _ = m.Update(api.RoomCreateFailed{Status: "INVALID_REQUEST", Message: "bad name", HTTPStatus: 400})
	got := next.(Model)
	if got.creating {
		t.Fatal("expected editable after rejection")
	}
	if !strings.Contains(got.headline, "Invalid request") {
		t.Fatalf("headline = %q", got.headline)
	}
	// The typed name survives so the user can fix it in place.
	if got.name.Value() != "Team Standup" {
		t.Fatalf("name = %q, want preserved", got.name.Value())
	}
}

func TestTransportFailureShowsServer(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "http://localhost:8080")
	m = typeIn(t, m, "Team Standup")
	next, _ := m.Update(keyMsg("enter"))
	m = next.(Model)

	next, _ = m.Update(api.RoomCreateFailed{Message: "connection refused"})
	got := next.(Model)
	if !strings.Contains(got.headline, "Cannot reach server") {
		t.Fatalf("headline = %q", got.headline)
	}
	if !strings.Contains(got.detail, "connection refused") {
		t.Fatalf("detail = %q", got.detail)
	}
}

func TestViewShowsHelpAndInFlightCopy(t *testing.T) {
	rec := &recorder{}
	m := New(rec.submit, "srv")
	if view := m.View(80, 24); !strings.Contains(view, "enter: create") {
		t.Fatalf("help line missing: %s", view)
	}
	m = typeIn(t, m, "Team Standup")
	next, _ := m.Update(keyMsg("enter"))
	if view := next.(Model).View(80, 24); !strings.Contains(view, "Creating room…") {
		t.Fatalf("in-flight copy missing: %s", view)
	}
}
