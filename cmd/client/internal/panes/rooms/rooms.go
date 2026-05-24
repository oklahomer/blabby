// Package rooms renders the left-side Rooms pane. It currently
// ships only the empty placeholder; the selectable room list will
// be added when room browsing lands.
package rooms

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// State holds the rendering inputs for the Rooms pane. It is empty
// today; the rooms slice and selection cursor will move in here
// when the pane learns to list joined rooms.
type State struct{}

// View renders the pane content. focused is honoured only for any
// inline accents (the border itself is drawn by the chrome).
func View(_ State, _ bool, _, _ int) string {
	title := ui.Title().Render("Rooms")
	placeholder := ui.Subtle().Render("(no rooms yet)")
	return lipgloss.JoinVertical(lipgloss.Left, title, "", placeholder)
}
