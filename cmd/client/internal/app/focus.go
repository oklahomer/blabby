// Package app holds the root tea.Model that orchestrates the chrome,
// panes, modals, and transport for the TUI client. Phase transitions
// — login modal open/closed, pane focus moves, websocket
// connect/disconnect — live exclusively in Update; sub-Models and
// helpers report typed outcomes which Update maps to the next state.
//
// The render path and modal overlay are exercised through the
// teatest integration test in app_test.go rather than via per-line
// unit tests, because lipgloss output is brittle to assert on. The
// per-package coverage on cmd/client/... is therefore weighted
// toward state-machine logic rather than render fidelity.
package app

// focusTarget enumerates the focusable panes. The right (Profile +
// Time) pane is read-only and never appears here.
type focusTarget int

const (
	focusRooms     focusTarget = iota // left pane
	focusMainView                     // middle: scrollback region
	focusMainInput                    // middle: input region
)

// focusOrder is the cycle tab/shift+tab walks through. Keep this in
// sync with the constants above.
var focusOrder = []focusTarget{focusRooms, focusMainView, focusMainInput}

// next returns the focusTarget that follows f under the tab cycle.
func (f focusTarget) next() focusTarget {
	for i, candidate := range focusOrder {
		if candidate == f {
			return focusOrder[(i+1)%len(focusOrder)]
		}
	}
	return focusOrder[0]
}

// prev returns the focusTarget that precedes f under shift+tab.
func (f focusTarget) prev() focusTarget {
	for i, candidate := range focusOrder {
		if candidate == f {
			return focusOrder[(i-1+len(focusOrder))%len(focusOrder)]
		}
	}
	return focusOrder[0]
}

// interpret maps a key (as its bubbletea KeyMsg.String() output) to
// a focus transition. The second return value indicates whether the
// key was consumed for focus management (true) or should be passed
// through to the focused pane (false). Direct jumps via ctrl+1/2/3
// take precedence over the tab/shift+tab cycle.
//
// Taking a string instead of a tea.KeyMsg keeps the function pure
// and trivially testable; the caller in Update performs the
// msg.String() conversion at the dispatch site.
func interpret(keyString string, current focusTarget) (focusTarget, bool) {
	switch keyString {
	case "ctrl+1":
		return focusRooms, true
	case "ctrl+2":
		return focusMainView, true
	case "ctrl+3":
		return focusMainInput, true
	case "tab":
		return current.next(), true
	case "shift+tab":
		return current.prev(), true
	}
	return current, false
}
