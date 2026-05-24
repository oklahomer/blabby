// Package modal defines the modal-overlay primitive used by every
// transient sub-view in the TUI client (login, future search,
// future join-confirm). The root app.Model holds at most one Modal
// at a time and routes input to it; the chrome continues to render
// behind the modal so the workspace context remains visible.
package modal

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Modal is a transient UI overlay that captures all input until
// dismissed. A modal owns its own rendering — the chrome stays
// visible behind it, and Overlay() centres the modal on screen.
//
// Update returns the next Modal state and an optional tea.Cmd.
// Returning a nil Modal value dismisses the modal; the root app
// clears its modal slot and routes subsequent input to the focused
// pane. To quit the program from a modal, return (nil, tea.Quit).
type Modal interface {
	Init() tea.Cmd
	Update(tea.Msg) (Modal, tea.Cmd)
	View(width, height int) string
}

// Overlay composites modalView on top of background, centred on the
// given screen dimensions. The background remains visible around
// the modal — implemented by slicing each background row with an
// ANSI-aware cut and splicing the modal line into the centred
// column range.
//
// Falls back to centring the modal on a blank canvas when the
// modal is larger than the screen, or when the screen dimensions
// are non-positive (e.g., before the first tea.WindowSizeMsg).
func Overlay(background, modalView string, screenW, screenH int) string {
	if screenW <= 0 || screenH <= 0 {
		return modalView
	}

	modalW := lipgloss.Width(modalView)
	modalH := lipgloss.Height(modalView)

	if modalW > screenW || modalH > screenH || background == "" {
		return lipgloss.Place(
			screenW, screenH,
			lipgloss.Center, lipgloss.Center,
			modalView,
			lipgloss.WithWhitespaceChars(" "),
		)
	}

	left := (screenW - modalW) / 2
	top := (screenH - modalH) / 2

	bgLines := strings.Split(background, "\n")
	for len(bgLines) < screenH {
		bgLines = append(bgLines, "")
	}

	modalLines := strings.Split(modalView, "\n")
	for i, modalLine := range modalLines {
		row := top + i
		if row < 0 || row >= len(bgLines) {
			continue
		}
		bgLine := bgLines[row]
		prefix := ansi.Cut(bgLine, 0, left)
		suffix := ansi.Cut(bgLine, left+modalW, screenW)
		bgLines[row] = prefix + modalLine + suffix
	}

	return strings.Join(bgLines, "\n")
}
