package mainview

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/oklahomer/blabby/cmd/client/internal/timeline"
)

func TestViewDefaultLabel(t *testing.T) {
	t.Parallel()
	out := View(State{}, false, 50, 20)
	if !strings.Contains(out, "(no room selected)") {
		t.Errorf("missing default room label:\n%s", out)
	}
	if !strings.Contains(out, "(no messages yet)") {
		t.Errorf("missing scrollback placeholder:\n%s", out)
	}
	if !strings.Contains(out, "(select a room to start typing)") {
		t.Errorf("missing input placeholder:\n%s", out)
	}
}

func TestViewRoomLabel(t *testing.T) {
	t.Parallel()
	out := View(State{RoomLabel: "general"}, false, 50, 20)
	if !strings.Contains(out, "general") {
		t.Errorf("missing room label:\n%s", out)
	}
}

func TestViewRendersMessagesInGivenOrder(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 5, 30, 9, 8, 7, 0, time.Local)
	out := View(State{
		RoomLabel: "general",
		Messages: []Message{
			{Sender: "alice", Text: "first-line", At: at},
			{Sender: "bob", Text: "second-line", At: at.Add(time.Minute)},
		},
	}, false, 60, 20)

	if !strings.Contains(out, "09:08:07") {
		t.Errorf("missing formatted timestamp:\n%s", out)
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "first-line") {
		t.Errorf("missing first message:\n%s", out)
	}
	i1 := strings.Index(out, "first-line")
	i2 := strings.Index(out, "second-line")
	if i1 < 0 || i2 < 0 || i1 > i2 {
		t.Errorf("messages not rendered in supplied order (first should precede second):\n%s", out)
	}
}

func TestViewZeroTimestampRendersPlaceholder(t *testing.T) {
	t.Parallel()
	out := View(State{
		RoomLabel: "general",
		Messages:  []Message{{Sender: "alice", Text: "hi"}},
	}, false, 60, 20)
	if !strings.Contains(out, zeroTimeGlyph) {
		t.Errorf("expected zero-time glyph %q:\n%s", zeroTimeGlyph, out)
	}
}

func TestViewOverflowKeepsNewestTail(t *testing.T) {
	t.Parallel()
	msgs := make([]Message, 0, 40)
	at := time.Date(2026, 5, 30, 0, 0, 0, 0, time.Local)
	for i := 0; i < 40; i++ {
		msgs = append(msgs, Message{
			Sender: "alice",
			Text:   "line-" + string(rune('A'+i%26)) + "-" + itoa(i),
			At:     at.Add(time.Duration(i) * time.Second),
		})
	}
	// A short pane: only a handful of rows are available for scrollback.
	out := View(State{RoomLabel: "general", Messages: msgs}, false, 60, 12)

	newest := "line-" + string(rune('A'+39%26)) + "-" + itoa(39)
	oldest := "line-A-0"
	if !strings.Contains(out, newest) {
		t.Errorf("newest message %q must remain visible:\n%s", newest, out)
	}
	if strings.Contains(out, oldest) {
		t.Errorf("oldest message %q should have been clipped from the top:\n%s", oldest, out)
	}
}

func TestVisibleLinesOffsetWindowsOlder(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 30, 9, 0, 0, 0, time.Local)
	lines := make([]Line, 0, 10)
	for i := 0; i < 10; i++ {
		lines = append(lines, Line{Msg: Message{ID: timeline.EventID(i), Kind: KindChat, At: base.Add(time.Duration(i) * time.Second)}})
	}
	height := reservedRows + 3 // avail == 3

	pinned := visibleLines(lines, height, false, 0)
	if len(pinned) != 3 || pinned[2].Msg.ID != 9 {
		t.Fatalf("offset 0 should show the newest 3 lines ending at id 9: %#v", pinned)
	}
	scrolled := visibleLines(lines, height, false, 2)
	if len(scrolled) != 3 || scrolled[0].Msg.ID != 5 || scrolled[2].Msg.ID != 7 {
		t.Fatalf("offset 2 should show ids 5..7: %#v", scrolled)
	}
	// Over-scroll clamps to the oldest window rather than running off-slice.
	over := visibleLines(lines, height, false, 999)
	if len(over) != 3 || over[0].Msg.ID != 0 {
		t.Fatalf("over-scroll should clamp to the oldest window: %#v", over)
	}
}

func TestViewOverflowErrorRowReservesScrollbackLine(t *testing.T) {
	t.Parallel()
	msgs := make([]Message, 0, 30)
	at := time.Date(2026, 5, 30, 0, 0, 0, 0, time.Local)
	for i := 0; i < 30; i++ {
		// Bracketed indices avoid prefix collisions: "[2]" is not a
		// substring of "[20]" or "[12]".
		msgs = append(msgs, Message{Sender: "a", Text: "[" + itoa(i) + "]", At: at.Add(time.Duration(i) * time.Second)})
	}
	const height = 12
	noErr := View(State{RoomLabel: "general", Messages: msgs}, false, 60, height)
	withErr := View(State{RoomLabel: "general", Messages: msgs, ErrorLine: "boom"}, false, 60, height)

	if !strings.Contains(withErr, "[29]") {
		t.Errorf("newest message must remain visible even with an error row:\n%s", withErr)
	}
	if !strings.Contains(withErr, "boom") {
		t.Errorf("error line must render:\n%s", withErr)
	}
	// The error row consumes one scrollback row, so the error variant
	// shows exactly one fewer message than the no-error variant.
	if vNo, vErr := countVisibleBracketed(noErr, 30), countVisibleBracketed(withErr, 30); vErr != vNo-1 {
		t.Errorf("error row should reserve exactly one scrollback row: noErr=%d withErr=%d", vNo, vErr)
	}
}

// forceColor pins a colour-capable profile for the duration of a test.
// lipgloss otherwise strips colour when it cannot detect a colour terminal
// (as under `go test`), which would make styled and unstyled output identical.
func forceColor(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// Deliberately NOT parallel: forceColor mutates lipgloss's process-global
// color profile, so this test must not overlap other tests in the package.
// Serial tests complete before any t.Parallel bodies start, which is what
// makes the global mutation safe.
func TestSelfMessageSenderIsMuted(t *testing.T) {
	forceColor(t)
	at := time.Date(2026, 5, 30, 9, 8, 7, 0, time.Local)
	mine := Message{Sender: "Rina", Text: "hi", At: at, Self: true}
	theirs := Message{Sender: "Rina", Text: "hi", At: at, Self: false}

	self := View(State{RoomLabel: "general", Messages: []Message{mine}}, false, 0, 0)
	other := View(State{RoomLabel: "general", Messages: []Message{theirs}}, false, 0, 0)

	// The name is always shown — Self never replaces it with "you".
	if !strings.Contains(self, "Rina") || !strings.Contains(other, "Rina") {
		t.Fatalf("sender name must appear in both renders:\nself=%q\nother=%q", self, other)
	}
	// Self styles the sender, so the two renders must differ.
	if self == other {
		t.Error("a Self message should render the sender differently (muted) than a non-self message")
	}
}

// Deliberately NOT parallel — see TestSelfMessageSenderIsMuted.
func TestSelfMessageRespectsWidth(t *testing.T) {
	forceColor(t) // emit real ANSI so the styled-name vs width-clip interaction is exercised
	at := time.Date(2026, 5, 30, 9, 8, 7, 0, time.Local)
	const width = 40
	// A long own message: the styled sender name must not defeat width clipping.
	out := View(State{
		RoomLabel: "general",
		Messages:  []Message{{Sender: "Rina", Text: strings.Repeat("x", 200), At: at, Self: true}},
	}, false, width, 0)

	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line exceeds width %d (visible width %d): %q", width, w, line)
		}
	}
}

func TestViewDisabledInputWhenCannotType(t *testing.T) {
	t.Parallel()
	out := View(State{RoomLabel: "general", Composer: "TYPED-COMPOSER", CanType: false}, false, 60, 20)
	if !strings.Contains(out, "(select a room to start typing)") {
		t.Errorf("expected disabled placeholder when CanType is false:\n%s", out)
	}
	if strings.Contains(out, "TYPED-COMPOSER") {
		t.Errorf("composer must not render when CanType is false:\n%s", out)
	}
}

func TestViewEnabledInputRendersComposer(t *testing.T) {
	t.Parallel()
	out := View(State{RoomLabel: "general", Composer: "TYPED-COMPOSER", CanType: true}, false, 60, 20)
	if !strings.Contains(out, "TYPED-COMPOSER") {
		t.Errorf("expected composer to render when CanType is true:\n%s", out)
	}
	if strings.Contains(out, "(select a room to start typing)") {
		t.Errorf("disabled placeholder must not render when CanType is true:\n%s", out)
	}
}

func TestViewConnectionStatusIndicator(t *testing.T) {
	t.Parallel()
	live := View(State{RoomLabel: "general", Connected: true}, false, 60, 20)
	if !strings.Contains(live, "● live") {
		t.Errorf("expected ● live indicator when connected:\n%s", live)
	}
	down := View(State{RoomLabel: "general", Connected: false}, false, 60, 20)
	if !strings.Contains(down, "● disconnected") {
		t.Errorf("expected ● disconnected indicator when not connected:\n%s", down)
	}
}

func TestViewStatusLineShowsLoadingHint(t *testing.T) {
	t.Parallel()
	loading := View(State{RoomLabel: "general", Connected: true, FetchingOlder: true}, false, 60, 20)
	if !strings.Contains(loading, "loading history") {
		t.Errorf("expected the loading-history hint while fetching:\n%s", loading)
	}
	idle := View(State{RoomLabel: "general", Connected: true, FetchingOlder: false}, false, 60, 20)
	if strings.Contains(idle, "loading history") {
		t.Errorf("loading hint must not show when not fetching:\n%s", idle)
	}
}

func TestViewRendersInlineError(t *testing.T) {
	t.Parallel()
	out := View(State{RoomLabel: "general", ErrorLine: "Not a member of this room"}, false, 60, 20)
	if !strings.Contains(out, "Not a member of this room") {
		t.Errorf("expected inline error line to render:\n%s", out)
	}
}

func TestViewRendersJoinedSystemLine(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 5, 30, 14, 22, 30, 0, time.Local)
	out := View(State{
		RoomLabel: "general",
		Messages:  []Message{{ID: 1, Kind: KindJoined, Sender: "bob", At: at}},
	}, false, 60, 20)
	if !strings.Contains(out, "14:22:30") {
		t.Errorf("system line missing timestamp:\n%s", out)
	}
	if !strings.Contains(out, "— bob joined —") {
		t.Errorf("expected joined system line body:\n%s", out)
	}
}

func TestViewRendersLeftSystemLine(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 5, 30, 14, 22, 30, 0, time.Local)
	out := View(State{
		RoomLabel: "general",
		Messages:  []Message{{ID: 1, Kind: KindLeft, Sender: "bob", At: at}},
	}, false, 60, 20)
	if !strings.Contains(out, "— bob left —") {
		t.Errorf("expected left system line body:\n%s", out)
	}
}

func TestLinesInsertsSeparatorOnDateChange(t *testing.T) {
	t.Parallel()
	day1 := time.Date(2026, 5, 30, 9, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 5, 31, 9, 0, 0, 0, time.Local)
	lines := Lines([]Message{
		{ID: 1, At: day1},
		{ID: 2, At: day1.Add(time.Minute)},
		{ID: 3, At: day2},
	})
	// Two same-day messages, then a separator, then the next day's message.
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (3 messages + 1 separator), got %d: %#v", len(lines), lines)
	}
	if lines[0].Separator || lines[1].Separator {
		t.Fatalf("no separator should precede the first day's messages: %#v", lines)
	}
	if !lines[2].Separator || lines[2].Date != "2026-05-31" {
		t.Fatalf("expected a 2026-05-31 separator at index 2: %#v", lines[2])
	}
	if lines[3].Separator || lines[3].Msg.ID != 3 {
		t.Fatalf("expected day-2 message after the separator: %#v", lines[3])
	}
}

func TestLinesNoSeparatorWithinOneDay(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 5, 30, 9, 0, 0, 0, time.Local)
	lines := Lines([]Message{{ID: 1, At: at}, {ID: 2, At: at.Add(time.Hour)}})
	for _, ln := range lines {
		if ln.Separator {
			t.Fatalf("no separator expected within a single day: %#v", lines)
		}
	}
}

func TestLinesZeroTimestampNeitherStartsNorBreaksARun(t *testing.T) {
	t.Parallel()
	day1 := time.Date(2026, 5, 30, 9, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 5, 31, 9, 0, 0, 0, time.Local)
	// A zero-At entry sits between two dated entries of different days: it
	// must not trigger its own separator, and the day change is still marked
	// against the last dated entry.
	lines := Lines([]Message{
		{ID: 1, At: day1},
		{ID: 2}, // zero At
		{ID: 3, At: day2},
	})
	sepCount := 0
	for _, ln := range lines {
		if ln.Separator {
			sepCount++
			if ln.Date != "2026-05-31" {
				t.Fatalf("separator date = %q, want 2026-05-31", ln.Date)
			}
		}
	}
	if sepCount != 1 {
		t.Fatalf("expected exactly one separator, got %d: %#v", sepCount, lines)
	}
}

func TestFormatMessageLineSanitizesControlRuns(t *testing.T) {
	t.Parallel()
	line := formatMessageLine(Message{Sender: "a\tb", Text: "one\ntwo\rthree", Kind: KindChat}, 0)
	if strings.ContainsAny(line, "\n\r\t") {
		t.Fatalf("control runs not sanitized to spaces: %q", line)
	}
}

// countVisibleBracketed returns how many of the bracketed message
// bodies "[0]".."[n-1]" appear in the rendered output.
func countVisibleBracketed(rendered string, n int) int {
	c := 0
	for i := 0; i < n; i++ {
		if strings.Contains(rendered, "["+itoa(i)+"]") {
			c++
		}
	}
	return c
}

// itoa is a tiny strconv.Itoa stand-in so the overflow fixture can
// build distinct message bodies without importing strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
