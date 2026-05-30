// Package mainview renders the middle Main pane: the active room's
// scrollback, a passive connection-status indicator, an inline error
// row, and the message composer (or a disabled placeholder when no
// room is selected). The pane is render-only — all state (the per-room
// message buckets, the composer's textinput.Model, the connected flag)
// lives in the root app.Model, which assembles a fresh State for every
// frame. The pane never reports anything back.
package mainview

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/oklahomer/blabby/cmd/client/internal/ui"
)

// zeroTimeGlyph is shown in place of a timestamp for messages whose
// server timestamp was the zero value (the server emits 0 for a
// zero-value time).
const zeroTimeGlyph = "--:--:--"

// reservedRows is the number of fixed rows View renders around the
// scrollback (room label, status line, the blank separator, the
// divider, and the input row). The error row, when present, costs one
// more; visibleMessages accounts for it via hasError.
const reservedRows = 5

// Message is one rendered scrollback entry. Sender is already resolved
// for display (the user's own messages may render as "you"); At is the
// server-assigned post time, zero when unknown.
type Message struct {
	Sender string
	Text   string
	At     time.Time
}

// State holds the rendering inputs for the Main pane. A zero State
// renders the "(no room selected)" / "(no messages yet)" / disabled
// composer placeholders, so the pane degrades cleanly before a room is
// active.
type State struct {
	// RoomLabel is the active room's display name, or "" pre-selection.
	RoomLabel string
	// Messages is the active room's scrollback, ordered oldest→newest.
	Messages []Message
	// Composer is the rendered textinput string. Shown only when
	// CanType is true; otherwise the disabled placeholder is shown.
	Composer string
	// ErrorLine is a humanised inline error, or "" when there is none.
	ErrorLine string
	// CanType reports whether a room is active and the composer is
	// usable. False renders the "(select a room to start typing)" hint.
	CanType bool
	// Connected drives the passive ● live / ● disconnected status line.
	Connected bool
}

// View renders the pane content for the given inner width and height.
// height is the pane's inner height (after the chrome border budget);
// when positive, only the newest scrollback lines that fit are rendered
// so the composer and divider are never clipped off the bottom. A
// non-positive height renders every message (used by unit tests and as
// a safe degrade when the layout is unavailable). focused is reserved
// for later focus-aware accents and is currently unused.
func View(s State, _ bool, width, height int) string {
	label := s.RoomLabel
	if label == "" {
		label = "(no room selected)"
	}

	parts := []string{
		ui.Title().Render(clip(label, width)),
		statusLine(s.Connected),
		"",
		scrollback(s.Messages, width, height, s.ErrorLine != ""),
	}
	if s.ErrorLine != "" {
		parts = append(parts, ui.Error().Render(clip(s.ErrorLine, width)))
	}
	parts = append(parts, divider(width), inputRow(s))

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// statusLine renders the passive connection indicator. It is purely
// informational — the actionable recovery path remains the login modal
// the root Model reopens on disconnect.
func statusLine(connected bool) string {
	if connected {
		return ui.Label().Render("● live")
	}
	return ui.Error().Render("● disconnected")
}

// scrollback renders the visible message lines (newest at the bottom),
// or the empty placeholder when there are no messages.
func scrollback(msgs []Message, width, height int, hasError bool) string {
	visible := visibleMessages(msgs, height, hasError)
	if len(visible) == 0 {
		return ui.Subtle().Render("(no messages yet)")
	}
	lines := make([]string, 0, len(visible))
	for _, m := range visible {
		lines = append(lines, clip(formatMessageLine(m), width))
	}
	return strings.Join(lines, "\n")
}

// visibleMessages returns the newest tail of msgs that fits the pane's
// inner height after the fixed rows are reserved. The newest messages
// sit at the bottom and are never the ones dropped; only the oldest
// overflow is hidden. A non-positive height returns every message.
func visibleMessages(msgs []Message, height int, hasError bool) []Message {
	if len(msgs) == 0 || height <= 0 {
		return msgs
	}
	reserved := reservedRows
	if hasError {
		reserved++
	}
	avail := height - reserved
	if avail < 1 {
		avail = 1
	}
	if len(msgs) <= avail {
		return msgs
	}
	return msgs[len(msgs)-avail:]
}

// formatMessageLine renders one scrollback row as "HH:MM:SS  sender  text".
// A zero timestamp renders the placeholder glyph instead of the epoch.
func formatMessageLine(m Message) string {
	ts := zeroTimeGlyph
	if !m.At.IsZero() {
		ts = m.At.Format("15:04:05")
	}
	return ts + "  " + m.Sender + "  " + m.Text
}

// inputRow renders the composer when typing is enabled, or the disabled
// placeholder when no room is active.
func inputRow(s State) string {
	if s.CanType {
		return s.Composer
	}
	return ui.Subtle().Render("(select a room to start typing)")
}

// divider renders the horizontal rule separating the scrollback from
// the input region, spanning the pane width when known.
func divider(width int) string {
	n := width
	if n <= 0 {
		n = 16
	}
	return strings.Repeat("─", n)
}

// clip truncates s to at most width runes. A non-positive width is a
// no-op so callers without layout information render the full string.
// Truncation operates on the raw (unstyled) string so it never splits
// an ANSI escape sequence.
func clip(s string, width int) string {
	if width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width])
}
