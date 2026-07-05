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

// MessageKind discriminates a chat line from the two membership system
// lines. It drives which formatter renders the entry.
type MessageKind int

const (
	// KindChat is a posted chat message: "HH:MM:SS  sender  text".
	KindChat MessageKind = iota
	// KindJoined is a member-joined system line: "HH:MM:SS  — name joined —".
	KindJoined
	// KindLeft is a member-left system line: "HH:MM:SS  — name left —".
	KindLeft
)

// Message is one rendered scrollback entry. ID is the timeline event id
// the scrollback is ordered and deduped by; it is opaque to rendering.
// Kind selects the chat or system-line formatter. Sender is the display
// name (the server-assigned name, or the raw code as a fallback). Self
// marks the viewing user's own chat messages, which render with a muted
// sender name so other members stand out. At is the server-assigned time,
// zero when unknown.
type Message struct {
	ID     int64
	Kind   MessageKind
	Sender string
	Text   string
	At     time.Time
	Self   bool
}

// Line is one physical scrollback row: either a message/system entry or a
// dim date separator inserted where the calendar day changes. Separators
// are derived at render time (see Lines) so ordering and dedup upstream
// stay oblivious to them.
type Line struct {
	Separator bool
	Date      string // set when Separator; the new day, "2006-01-02"
	Msg       Message
}

// lineBreaks collapses the runs that would break the one-row-per-entry
// invariant the scrollback windowing depends on.
var lineBreaks = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")

// sanitize replaces embedded newlines, carriage returns, and tabs with
// single spaces so a multiline body cannot spill across the fixed row
// budget the height math reserves.
func sanitize(s string) string { return lineBreaks.Replace(s) }

// Lines derives the physical rows for msgs (ordered oldest→newest),
// inserting a dim date separator before the first entry of each new
// calendar day. The comparison is against the most recent dated entry, so
// a zero-timestamp entry neither starts nor breaks a run, and there is
// never a leading separator above the first entry.
func Lines(msgs []Message) []Line {
	out := make([]Line, 0, len(msgs))
	lastDate := ""
	for _, m := range msgs {
		if !m.At.IsZero() {
			d := m.At.Format("2006-01-02")
			if lastDate != "" && d != lastDate {
				out = append(out, Line{Separator: true, Date: d})
			}
			lastDate = d
		}
		out = append(out, Line{Msg: m})
	}
	return out
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
	// FetchingOlder appends a subtle "(loading history…)" hint to the
	// status line while a backfill page is in flight for the active room.
	FetchingOlder bool
	// Offset is how many rows the scrollback is scrolled up from the newest
	// line; 0 pins the view to the bottom. It is clamped to the scrollable
	// range at render time.
	Offset int
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
		statusLine(s.Connected, s.FetchingOlder),
		"",
		scrollback(s.Messages, width, height, s.ErrorLine != "", s.Offset),
	}
	if s.ErrorLine != "" {
		parts = append(parts, ui.Error().Render(clip(s.ErrorLine, width)))
	}
	parts = append(parts, divider(width), inputRow(s))

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// statusLine renders the passive connection indicator, with a subtle
// "(loading history…)" hint appended while a backfill is in flight. It is
// purely informational — the actionable recovery path remains the login
// modal the root Model reopens on disconnect.
func statusLine(connected, fetchingOlder bool) string {
	if !connected {
		return ui.Error().Render("● disconnected")
	}
	line := ui.Label().Render("● live")
	if fetchingOlder {
		line += ui.Subtle().Render("  (loading history…)")
	}
	return line
}

// scrollback renders the visible rows (newest at the bottom, unless
// scrolled up by offset), or the empty placeholder when there are no
// messages. Messages are expanded to physical rows (with date separators)
// before windowing so a separator occupies a row like any other line.
func scrollback(msgs []Message, width, height int, hasError bool, offset int) string {
	visible := visibleLines(Lines(msgs), height, hasError, offset)
	if len(visible) == 0 {
		return ui.Subtle().Render("(no messages yet)")
	}
	rows := make([]string, 0, len(visible))
	for _, ln := range visible {
		rows = append(rows, renderLine(ln, width))
	}
	return strings.Join(rows, "\n")
}

// renderLine renders one physical row: a date separator, a chat message,
// or a membership system line.
func renderLine(ln Line, width int) string {
	switch {
	case ln.Separator:
		return formatSeparatorLine(ln.Date, width)
	case ln.Msg.Kind == KindChat:
		return formatMessageLine(ln.Msg, width)
	default:
		return formatSystemLine(ln.Msg, width)
	}
}

// visibleLines returns the window of lines that fits the pane's inner
// height after the fixed rows are reserved. offset scrolls the window up
// from the bottom (0 pins to the newest line); it is clamped to the
// scrollable range so an over-scroll shows the oldest lines rather than
// running off the slice. A non-positive height returns every line.
func visibleLines(lines []Line, height int, hasError bool, offset int) []Line {
	if len(lines) == 0 || height <= 0 {
		return lines
	}
	avail := availableRows(height, hasError)
	if len(lines) <= avail {
		return lines
	}
	offset = clampWindowOffset(offset, len(lines)-avail)
	end := len(lines) - offset
	return lines[end-avail : end]
}

// VisibleCapacity is how many scrollback rows fit the pane's inner height
// after the fixed rows (and the optional error row) are reserved. The root
// Model uses it to clamp the scroll offset and size a page jump.
func VisibleCapacity(height int, hasError bool) int {
	return availableRows(height, hasError)
}

// availableRows is the scrollback row budget for the given inner height.
func availableRows(height int, hasError bool) int {
	reserved := reservedRows
	if hasError {
		reserved++
	}
	if avail := height - reserved; avail > 1 {
		return avail
	}
	return 1
}

// clampWindowOffset bounds offset into [0, maxOffset].
func clampWindowOffset(offset, maxOffset int) int {
	if offset < 0 {
		return 0
	}
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

// formatMessageLine renders one scrollback row as "HH:MM:SS  sender  text",
// clipped to width. A zero timestamp renders the placeholder glyph instead of
// the epoch. The viewing user's own messages render with a muted sender name.
//
// Width is enforced by clipping only the trailing text, so the styled (ANSI)
// sender name is never fed to rune-based truncation. When the pane is too
// narrow to fit even the "HH:MM:SS  sender  " prefix, the whole line is
// clipped unstyled — a rare, graceful degrade rather than a split escape
// sequence.
func formatMessageLine(m Message, width int) string {
	ts := timestampGlyph(m.At)
	name := sanitize(m.Sender)

	prefix := ts + "  " + name + "  "
	if width > 0 && len([]rune(prefix)) >= width {
		return clip(ts+"  "+name+"  "+sanitize(m.Text), width)
	}

	text := sanitize(m.Text)
	if width > 0 {
		text = clip(text, width-len([]rune(prefix)))
	}

	sender := name
	if m.Self {
		// Mute the viewer's own name (light grey) so other members read louder.
		sender = ui.Subtle().Render(name)
	}
	return ts + "  " + sender + "  " + text
}

// formatSystemLine renders a membership event as a dim inline em-dash row,
// "HH:MM:SS  — name joined —" (or "left"). The timestamp is rendered plain
// like a chat row; only the em-dash body is muted. Width is enforced by
// clipping the trailing body so the styled body never feeds rune-based
// truncation, mirroring formatMessageLine.
func formatSystemLine(m Message, width int) string {
	ts := timestampGlyph(m.At)
	verb := "joined"
	if m.Kind == KindLeft {
		verb = "left"
	}
	body := "— " + sanitize(m.Sender) + " " + verb + " —"

	prefix := ts + "  "
	if width > 0 && len([]rune(prefix)) >= width {
		return clip(prefix+body, width)
	}
	if width > 0 {
		body = clip(body, width-len([]rune(prefix)))
	}
	return prefix + ui.Subtle().Render(body)
}

// formatSeparatorLine renders a dim date divider, "— 2006-01-02 —",
// clipped to width.
func formatSeparatorLine(date string, width int) string {
	return ui.Subtle().Render(clip("— "+date+" —", width))
}

// timestampGlyph formats a post time as HH:MM:SS, or the placeholder glyph
// for a zero time.
func timestampGlyph(at time.Time) string {
	if at.IsZero() {
		return zeroTimeGlyph
	}
	return at.Format("15:04:05")
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
