package rooms

import (
	"strings"
	"testing"
)

func TestViewEmptyShowsPlaceholderAndHint(t *testing.T) {
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
	out := View(State{Loading: true}, false, 20, 20)
	if !strings.Contains(out, "(loading…)") {
		t.Errorf("expected loading hint:\n%s", out)
	}
	if strings.Contains(out, "(no rooms yet)") {
		t.Errorf("loading state must not show the empty placeholder:\n%s", out)
	}
}

func TestViewLoadErrorShowsRetryHint(t *testing.T) {
	out := View(State{LoadError: "Server unavailable — please try again"}, true, 20, 20)
	if !strings.Contains(out, "(failed to load rooms)") {
		t.Errorf("expected failure line:\n%s", out)
	}
	if !strings.Contains(out, "(press r to retry)") {
		t.Errorf("expected retry hint:\n%s", out)
	}
}

func TestViewPopulatedRendersRows(t *testing.T) {
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
	_, outcome := HandleKey(State{JoinedIDs: []string{"a", "b"}, Cursor: 0}, "enter")
	if outcome != OutcomeSwitchActiveRoom {
		t.Fatalf("expected OutcomeSwitchActiveRoom, got %v", outcome)
	}
}

func TestHandleKeyRetryOnlyWhenLoadError(t *testing.T) {
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
	if got := (State{JoinedIDs: []string{"a", "b"}, Cursor: 1}).ActiveID(); got != "b" {
		t.Errorf("got %q, want b", got)
	}
	if got := (State{}).ActiveID(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResolveNameFallsBackToID(t *testing.T) {
	state := State{NameForID: map[string]string{"general": "General"}}
	if got := state.ResolveName("general"); got != "General" {
		t.Errorf("got %q, want General", got)
	}
	if got := state.ResolveName("random"); got != "random" {
		t.Errorf("got %q, want random (verbatim fallback)", got)
	}
}
