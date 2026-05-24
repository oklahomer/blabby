package chrome

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// State carries everything Render needs to draw the three-pane chrome.
// The caller (root app.Model.View) provides each pane's already-rendered
// string content; chrome composes the borders and arrangement around
// them. Keeping pane rendering out of this package preserves the
// "panes own their View" boundary documented in the package layout.
type State struct {
	Width  int
	Height int

	RoomsView    string
	MainView     string
	InfoView     string
	FocusedRooms bool
	FocusedMain  bool
}

// Render composes the three-pane frame for the given State. On
// ErrTerminalTooSmall, it returns a single resize-prompt screen
// sized to the terminal so the alternate screen does not flicker
// with partial content while the user resizes.
func Render(s State) string {
	layout, err := Compute(s.Width, s.Height)
	if err != nil {
		return renderTooSmall(s.Width, s.Height)
	}

	leftStyle := ui.PaneBorder(s.FocusedRooms).
		Width(innerWidth(layout.LeftW)).
		Height(innerHeight(layout.Height))
	middleStyle := ui.PaneBorder(s.FocusedMain).
		Width(innerWidth(layout.MiddleW)).
		Height(innerHeight(layout.Height))
	rightStyle := ui.PaneBorder(false).
		Width(innerWidth(layout.RightW)).
		Height(innerHeight(layout.Height))

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		leftStyle.Render(s.RoomsView),
		middleStyle.Render(s.MainView),
		rightStyle.Render(s.InfoView),
	)
}

// innerWidth subtracts the two-column border budget from the pane's
// allocated total. Floors at 1 so very narrow panes still render.
func innerWidth(total int) int {
	w := total - 2
	if w < 1 {
		return 1
	}
	return w
}

// innerHeight subtracts the two-row border budget from the chrome's
// total height. Floors at 1 for the same reason as innerWidth.
func innerHeight(total int) int {
	h := total - 2
	if h < 1 {
		return 1
	}
	return h
}

// renderTooSmall produces the "Terminal too small" full-screen
// notice. It centers a short message inside a box sized to the
// current dimensions so the alternate screen does not show stale
// half-frames during a resize.
func renderTooSmall(width, height int) string {
	if width <= 0 {
		width = MinWidth
	}
	if height <= 0 {
		height = MinHeight
	}
	msg := ui.Error().Render("Terminal too small — please resize")
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, msg)
}
