// Package info renders the right-side Profile + Time pane. Profile
// is populated from the session (filled post-auth); the Time section
// shows a live HH:MM:SS clock and an ISO date, both in local time.
package info

import (
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// State holds the values the Info pane renders. Email and UserID
// are blank pre-auth and populated when the WS handshake completes;
// Server is set from the --server flag at launch.
type State struct {
	Email  string
	UserID string
	Server string
	Now    time.Time
}

// View renders Profile (User / ID / Server) and Time (clock + date)
// stacked vertically. The focused argument is accepted for symmetry
// with the other pane Views but is unused — the right pane is
// read-only and never receives focus.
func View(s State, _ bool, _, _ int) string {
	profile := lipgloss.JoinVertical(
		lipgloss.Left,
		ui.Title().Render("Profile"),
		"──────",
		ui.Label().Render("Email:  ")+s.Email,
		ui.Label().Render("ID:     ")+s.UserID,
		ui.Label().Render("Server: ")+s.Server,
	)

	now := s.Now
	if now.IsZero() {
		now = time.Now()
	}
	timeBlock := lipgloss.JoinVertical(
		lipgloss.Left,
		ui.Title().Render("Time"),
		"──────",
		now.Format("15:04:05"),
		now.Format("2006-01-02"),
	)

	return lipgloss.JoinVertical(lipgloss.Left, profile, "", timeBlock)
}
