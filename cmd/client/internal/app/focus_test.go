package app

import "testing"

func TestFocusNextPrevCycles(t *testing.T) {
	if got := focusRooms.next(); got != focusMainView {
		t.Errorf("rooms.next = %v, want focusMainView", got)
	}
	if got := focusMainView.next(); got != focusMainInput {
		t.Errorf("mainview.next = %v, want focusMainInput", got)
	}
	if got := focusMainInput.next(); got != focusRooms {
		t.Errorf("maininput.next = %v, want focusRooms (wrap)", got)
	}
	if got := focusRooms.prev(); got != focusMainInput {
		t.Errorf("rooms.prev = %v, want focusMainInput (wrap)", got)
	}
	if got := focusMainView.prev(); got != focusRooms {
		t.Errorf("mainview.prev = %v, want focusRooms", got)
	}
}

func TestInterpret(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		start    focusTarget
		want     focusTarget
		consumed bool
	}{
		{"tab from rooms → main view", "tab", focusRooms, focusMainView, true},
		{"tab from main view → main input", "tab", focusMainView, focusMainInput, true},
		{"tab from main input wraps → rooms", "tab", focusMainInput, focusRooms, true},
		{"shift+tab from rooms wraps → main input", "shift+tab", focusRooms, focusMainInput, true},
		{"shift+tab from main input → main view", "shift+tab", focusMainInput, focusMainView, true},
		{"ctrl+1 → rooms", "ctrl+1", focusMainInput, focusRooms, true},
		{"ctrl+2 → main view", "ctrl+2", focusRooms, focusMainView, true},
		{"ctrl+3 → main input", "ctrl+3", focusRooms, focusMainInput, true},
		{"unknown key not consumed", "x", focusRooms, focusRooms, false},
		{"enter not consumed", "enter", focusMainInput, focusMainInput, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, consumed := interpret(tc.key, tc.start)
			if got != tc.want {
				t.Errorf("focus = %v, want %v", got, tc.want)
			}
			if consumed != tc.consumed {
				t.Errorf("consumed = %v, want %v", consumed, tc.consumed)
			}
		})
	}
}
