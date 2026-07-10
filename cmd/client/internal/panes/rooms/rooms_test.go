package rooms

import (
	"strings"
	"testing"
)

func TestViewEmptyShowsPlaceholderAndHint(t *testing.T) {
	t.Parallel()
	out := View(State{}, false, 20, 20)
	if !strings.Contains(out, "Rooms") {
		t.Errorf("missing Rooms title:\n%s", out)
	}
	if !strings.Contains(out, "(no rooms yet)") {
		t.Errorf("missing empty placeholder:\n%s", out)
	}
	if !strings.Contains(out, "(press / to search)") {
		t.Errorf("missing search hint:\n%s", out)
	}
}

func TestViewLoadingHidesEmptyPlaceholder(t *testing.T) {
	t.Parallel()
	out := View(State{Loading: true}, false, 20, 20)
	if !strings.Contains(out, "(loading…)") {
		t.Errorf("expected loading hint:\n%s", out)
	}
	if strings.Contains(out, "(no rooms yet)") {
		t.Errorf("loading state must not show the empty placeholder:\n%s", out)
	}
}

func TestViewLoadErrorShowsRetryHint(t *testing.T) {
	t.Parallel()
	out := View(State{LoadError: "Server unavailable — please try again"}, true, 20, 20)
	if !strings.Contains(out, "(failed to load rooms)") {
		t.Errorf("expected failure line:\n%s", out)
	}
	if !strings.Contains(out, "(press r to retry)") {
		t.Errorf("expected retry hint:\n%s", out)
	}
}

func TestViewPopulatedRendersRows(t *testing.T) {
	t.Parallel()
	state := State{
		JoinedIDs: []string{"general", "random"},
		Cursor:    1,
		NameForID: map[string]string{"general": "General"},
	}
	out := View(state, true, 30, 30)
	if !strings.Contains(out, "General") {
		t.Errorf("expected resolved name for general:\n%s", out)
	}
	if !strings.Contains(out, "random") {
		t.Errorf("expected unresolved id random:\n%s", out)
	}
}

func TestHandleKeyCursorNavigation(t *testing.T) {
	t.Parallel()
	base := State{JoinedIDs: []string{"a", "b", "c"}}

	tests := []struct {
		name        string
		startState  State
		key         string
		wantCursor  int
		wantOutcome Outcome
	}{
		{"down advances", base, "down", 1, OutcomeNone},
		{"j advances", base, "j", 1, OutcomeNone},
		{"up at top stays", base, "up", 0, OutcomeNone},
		{"k at top stays", base, "k", 0, OutcomeNone},
		{"down at last clamps", State{JoinedIDs: []string{"a", "b", "c"}, Cursor: 2}, "down", 2, OutcomeNone},
		{"up from middle decrements", State{JoinedIDs: []string{"a", "b", "c"}, Cursor: 1}, "up", 0, OutcomeNone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			next, outcome := HandleKey(tc.startState, tc.key)
			if next.Cursor != tc.wantCursor {
				t.Errorf("cursor = %d, want %d", next.Cursor, tc.wantCursor)
			}
			if outcome != tc.wantOutcome {
				t.Errorf("outcome = %v, want %v", outcome, tc.wantOutcome)
			}
		})
	}
}

func TestHandleKeyOnEmptyListIgnoresMovementAndEnter(t *testing.T) {
	t.Parallel()
	_, outcome := HandleKey(State{}, "enter")
	if outcome != OutcomeNone {
		t.Fatalf("enter on empty list should not produce an outcome, got %v", outcome)
	}
	next, _ := HandleKey(State{}, "down")
	if next.Cursor != 0 {
		t.Fatalf("cursor mutated on empty list: %d", next.Cursor)
	}
}

func TestHandleKeyEnterReturnsSwitchActiveOutcome(t *testing.T) {
	t.Parallel()
	_, outcome := HandleKey(State{JoinedIDs: []string{"a", "b"}, Cursor: 0}, "enter")
	if outcome != OutcomeSwitchActiveRoom {
		t.Fatalf("expected OutcomeSwitchActiveRoom, got %v", outcome)
	}
}

func TestHandleKeyRetryOnlyWhenLoadError(t *testing.T) {
	t.Parallel()
	t.Run("with error returns retry", func(t *testing.T) {
		_, outcome := HandleKey(State{LoadError: "boom"}, "r")
		if outcome != OutcomeRetryLoad {
			t.Fatalf("expected OutcomeRetryLoad, got %v", outcome)
		}
	})
	t.Run("without error is no-op", func(t *testing.T) {
		_, outcome := HandleKey(State{JoinedIDs: []string{"a"}}, "r")
		if outcome != OutcomeNone {
			t.Fatalf("expected OutcomeNone, got %v", outcome)
		}
	})
}

func TestHandleKeyUnknownKeyIsPassThrough(t *testing.T) {
	t.Parallel()
	state := State{JoinedIDs: []string{"a"}, Cursor: 0}
	next, outcome := HandleKey(state, "x")
	if outcome != OutcomeNone {
		t.Fatalf("expected OutcomeNone, got %v", outcome)
	}
	if next.Cursor != 0 {
		t.Fatalf("cursor mutated: %d", next.Cursor)
	}
}

func TestActiveIDReturnsCursorOrEmpty(t *testing.T) {
	t.Parallel()
	if got := (State{JoinedIDs: []string{"a", "b"}, Cursor: 1}).ActiveID(); got != "b" {
		t.Errorf("got %q, want b", got)
	}
	if got := (State{}).ActiveID(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestClampCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   State
		want int
	}{
		{"empty", State{Cursor: 5}, 0},
		{"negative", State{JoinedIDs: []string{"a", "b"}, Cursor: -1}, 0},
		{"in range", State{JoinedIDs: []string{"a", "b"}, Cursor: 1}, 1},
		{"past end", State{JoinedIDs: []string{"a", "b"}, Cursor: 5}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.ClampCursor().Cursor; got != tc.want {
				t.Fatalf("Cursor = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolveNameFallsBackToID(t *testing.T) {
	t.Parallel()
	state := State{NameForID: map[string]string{"general": "General"}}
	if got := state.ResolveName("general"); got != "General" {
		t.Errorf("got %q, want General", got)
	}
	if got := state.ResolveName("random"); got != "random" {
		t.Errorf("got %q, want random (verbatim fallback)", got)
	}
}

func TestLeaveGestureTwoPressConfirm(t *testing.T) {
	t.Parallel()
	s := State{JoinedIDs: []string{"general", "random"}, Cursor: 1}

	// First x arms the confirmation for the room under the cursor.
	s, outcome := HandleKey(s, "x")
	if outcome != OutcomeNone || s.PendingLeaveID != "random" {
		t.Fatalf("first x: outcome=%v pending=%q, want armed for random", outcome, s.PendingLeaveID)
	}
	// Second x on the same room confirms.
	s, outcome = HandleKey(s, "x")
	if outcome != OutcomeLeaveRoom {
		t.Fatalf("second x: outcome=%v, want OutcomeLeaveRoom", outcome)
	}
	if s.PendingLeaveID != "" {
		t.Fatalf("pending not cleared after confirm: %q", s.PendingLeaveID)
	}
	if s.ActiveID() != "random" {
		t.Fatalf("ActiveID = %q, want the confirmed room", s.ActiveID())
	}
}

func TestLeaveGestureDisarmedByOtherKeys(t *testing.T) {
	t.Parallel()
	s := State{JoinedIDs: []string{"general", "random"}, Cursor: 0}
	s, _ = HandleKey(s, "x")
	if s.PendingLeaveID != "general" {
		t.Fatalf("setup: pending=%q", s.PendingLeaveID)
	}
	// Moving the cursor disarms; the x that follows re-arms for the new row
	// instead of confirming the old one.
	s, _ = HandleKey(s, "down")
	if s.PendingLeaveID != "" {
		t.Fatalf("movement must disarm, pending=%q", s.PendingLeaveID)
	}
	s, outcome := HandleKey(s, "x")
	if outcome != OutcomeNone || s.PendingLeaveID != "random" {
		t.Fatalf("re-arm: outcome=%v pending=%q", outcome, s.PendingLeaveID)
	}
}

func TestLeaveOnEmptyListIsNoOp(t *testing.T) {
	t.Parallel()
	s, outcome := HandleKey(State{}, "x")
	if outcome != OutcomeNone || s.PendingLeaveID != "" {
		t.Fatalf("x on empty list: outcome=%v pending=%q", outcome, s.PendingLeaveID)
	}
}

func TestKeyClearsActionError(t *testing.T) {
	t.Parallel()
	s := State{JoinedIDs: []string{"general"}, ActionError: "Transfer ownership before leaving this room"}
	s, _ = HandleKey(s, "down")
	if s.ActionError != "" {
		t.Fatalf("ActionError not cleared: %q", s.ActionError)
	}
}

func TestPageAndEdgeNavigation(t *testing.T) {
	t.Parallel()
	ids := make([]string, 25)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}
	s := State{JoinedIDs: ids}

	s, _ = HandleKey(s, "pgdown")
	if s.Cursor != 10 {
		t.Fatalf("pgdown cursor = %d, want 10", s.Cursor)
	}
	s, _ = HandleKey(s, "end")
	if s.Cursor != 24 {
		t.Fatalf("end cursor = %d, want 24", s.Cursor)
	}
	s, _ = HandleKey(s, "pgup")
	if s.Cursor != 14 {
		t.Fatalf("pgup cursor = %d, want 14", s.Cursor)
	}
	s, _ = HandleKey(s, "home")
	if s.Cursor != 0 {
		t.Fatalf("home cursor = %d, want 0", s.Cursor)
	}
}

func TestViewRendersLeaveConfirmAndActionError(t *testing.T) {
	t.Parallel()
	s := State{
		JoinedIDs:      []string{"general"},
		NameForID:      map[string]string{"general": "General"},
		PendingLeaveID: "general",
	}
	out := View(s, true, 0, 0)
	if !strings.Contains(out, "press x again to leave") || !strings.Contains(out, "General") {
		t.Fatalf("confirm rows missing:\n%s", out)
	}

	s.PendingLeaveID = ""
	s.ActionError = "Transfer ownership before leaving this room"
	out = View(s, true, 0, 0)
	if !strings.Contains(out, "Transfer ownership") {
		t.Fatalf("action error missing:\n%s", out)
	}
}
