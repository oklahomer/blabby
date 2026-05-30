package mainview

import (
	"strings"
	"testing"
	"time"
)

func TestViewDefaultLabel(t *testing.T) {
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
	out := View(State{RoomLabel: "general"}, false, 50, 20)
	if !strings.Contains(out, "general") {
		t.Errorf("missing room label:\n%s", out)
	}
}

func TestViewRendersMessagesInGivenOrder(t *testing.T) {
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
	out := View(State{
		RoomLabel: "general",
		Messages:  []Message{{Sender: "alice", Text: "hi"}},
	}, false, 60, 20)
	if !strings.Contains(out, zeroTimeGlyph) {
		t.Errorf("expected zero-time glyph %q:\n%s", zeroTimeGlyph, out)
	}
}

func TestViewOverflowKeepsNewestTail(t *testing.T) {
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

func TestViewOverflowErrorRowReservesScrollbackLine(t *testing.T) {
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

func TestViewDisabledInputWhenCannotType(t *testing.T) {
	out := View(State{RoomLabel: "general", Composer: "TYPED-COMPOSER", CanType: false}, false, 60, 20)
	if !strings.Contains(out, "(select a room to start typing)") {
		t.Errorf("expected disabled placeholder when CanType is false:\n%s", out)
	}
	if strings.Contains(out, "TYPED-COMPOSER") {
		t.Errorf("composer must not render when CanType is false:\n%s", out)
	}
}

func TestViewEnabledInputRendersComposer(t *testing.T) {
	out := View(State{RoomLabel: "general", Composer: "TYPED-COMPOSER", CanType: true}, false, 60, 20)
	if !strings.Contains(out, "TYPED-COMPOSER") {
		t.Errorf("expected composer to render when CanType is true:\n%s", out)
	}
	if strings.Contains(out, "(select a room to start typing)") {
		t.Errorf("disabled placeholder must not render when CanType is true:\n%s", out)
	}
}

func TestViewConnectionStatusIndicator(t *testing.T) {
	live := View(State{RoomLabel: "general", Connected: true}, false, 60, 20)
	if !strings.Contains(live, "● live") {
		t.Errorf("expected ● live indicator when connected:\n%s", live)
	}
	down := View(State{RoomLabel: "general", Connected: false}, false, 60, 20)
	if !strings.Contains(down, "● disconnected") {
		t.Errorf("expected ● disconnected indicator when not connected:\n%s", down)
	}
}

func TestViewRendersInlineError(t *testing.T) {
	out := View(State{RoomLabel: "general", ErrorLine: "Not a member of this room"}, false, 60, 20)
	if !strings.Contains(out, "Not a member of this room") {
		t.Errorf("expected inline error line to render:\n%s", out)
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
