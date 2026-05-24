// Package mainview renders the middle Main pane. It currently
// ships the placeholder scrollback region and a disabled input
// region; both will gain real content (scrollback viewport,
// enabled textarea) when send/receive messaging lands.
package mainview

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// State holds the rendering inputs for the Main pane. It carries
// only the active-room label today (defaulted to "(no room
// selected)" pre-join); the scrollback and input buffers will
// move in here when messaging is wired up.
type State struct {
	RoomLabel string
}

// View renders the pane content with placeholder scrollback and a
// disabled input footer. The focused argument is reserved for
// later focus-aware accents and is currently unused.
func View(s State, _ bool, _, _ int) string {
	label := s.RoomLabel
	if label == "" {
		label = "(no room selected)"
	}
	header := ui.Title().Render(label)
	scrollback := ui.Subtle().Render("(no messages yet)")
	input := ui.Subtle().Render("(select a room to start typing)")
	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		scrollback,
		"",
		"────────────────",
		input,
	)
}
