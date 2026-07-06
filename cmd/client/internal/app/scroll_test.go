package app

import (
	"testing"
	"time"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
	"github.com/oklahomer/blabby/cmd/client/internal/timeline"
)

// scrollBase anchors every scroll-test message to one calendar day so line
// count equals message count (no date separators to complicate the math).
var scrollBase = time.Date(2026, 5, 30, 9, 0, 0, 0, time.Local)

func chatLine(id int64) mainview.Line {
	return mainview.Line{Msg: mainview.Message{ID: timeline.EventID(id), Kind: mainview.KindChat}}
}

func TestClampOffset(t *testing.T) {
	cases := []struct{ off, max, want int }{
		{-1, 5, 0}, {0, 5, 0}, {3, 5, 3}, {5, 5, 5}, {9, 5, 5},
	}
	for _, c := range cases {
		if got := clampOffset(c.off, c.max); got != c.want {
			t.Errorf("clampOffset(%d,%d) = %d, want %d", c.off, c.max, got, c.want)
		}
	}
}

func TestMaxScrollOffset(t *testing.T) {
	if got := maxScrollOffset(3, 10); got != 0 {
		t.Errorf("everything fits should give 0, got %d", got)
	}
	if got := maxScrollOffset(30, 10); got != 20 {
		t.Errorf("maxScrollOffset(30,10) = %d, want 20", got)
	}
}

func TestPageStep(t *testing.T) {
	if got := pageStep(1); got != 1 {
		t.Errorf("pageStep(1) = %d, want 1", got)
	}
	if got := pageStep(5); got != 4 {
		t.Errorf("pageStep(5) = %d, want 4", got)
	}
}

func TestAdjustOffsetPinnedStaysPinned(t *testing.T) {
	old := []mainview.Line{chatLine(10), chatLine(20)}
	next := []mainview.Line{chatLine(10), chatLine(20), chatLine(30)}
	if got := adjustOffset(old, next, 0); got != 0 {
		t.Errorf("a pinned view must stay pinned, got %d", got)
	}
}

func TestAdjustOffsetPrependHolds(t *testing.T) {
	// A backfill prepends older ids above the viewport; the bottom is
	// unchanged, so the offset must not move.
	old := []mainview.Line{chatLine(10), chatLine(20)}
	next := []mainview.Line{chatLine(5), chatLine(10), chatLine(20)}
	if got := adjustOffset(old, next, 1); got != 1 {
		t.Errorf("prepend should hold the offset at 1, got %d", got)
	}
}

func TestAdjustOffsetAppendSlides(t *testing.T) {
	// A live frame appends a newer id below the viewport; the offset slides
	// up by the growth so the anchored content stays in view.
	old := []mainview.Line{chatLine(10), chatLine(20)}
	next := []mainview.Line{chatLine(10), chatLine(20), chatLine(30)}
	if got := adjustOffset(old, next, 1); got != 2 {
		t.Errorf("append should slide the offset to 2, got %d", got)
	}
}

// scrollModel returns a room-active model sized for a real layout, with n
// same-day chat messages (ids 1..n) so the scroll math is predictable.
func scrollModel(t *testing.T, n int) Model {
	t.Helper()
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	msgs := make([]mainview.Message, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, mainview.Message{ID: timeline.EventID(i + 1), Kind: mainview.KindChat, At: scrollBase.Add(time.Duration(i) * time.Second)})
	}
	m.messages["general"] = msgs
	return m
}

func TestHandleScrollKeyMovesAndClamps(t *testing.T) {
	m := scrollModel(t, 100)
	avail := m.scrollAvail()
	maxOff := maxScrollOffset(len(m.activeLines()), avail)
	if maxOff < 3 {
		t.Fatalf("test needs a scrollable pane; maxOffset = %d", maxOff)
	}

	up, _ := m.handleScrollKey("up")
	if up.scrollOffset != 1 {
		t.Fatalf("up from bottom = %d, want 1", up.scrollOffset)
	}
	down, _ := up.handleScrollKey("down")
	if down.scrollOffset != 0 {
		t.Fatalf("down back to bottom = %d, want 0", down.scrollOffset)
	}
	// down at the bottom clamps at 0.
	if pinned, _ := down.handleScrollKey("down"); pinned.scrollOffset != 0 {
		t.Fatalf("down at bottom must clamp at 0, got %d", pinned.scrollOffset)
	}
	// home jumps to the top; end returns to the bottom.
	top, _ := m.handleScrollKey("home")
	if top.scrollOffset != maxOff {
		t.Fatalf("home = %d, want maxOffset %d", top.scrollOffset, maxOff)
	}
	end, _ := top.handleScrollKey("end")
	if end.scrollOffset != 0 {
		t.Fatalf("end = %d, want 0", end.scrollOffset)
	}
}

func TestHandleScrollKeyFetchesOlderAtTop(t *testing.T) {
	m := scrollModel(t, 100)
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "1"}}
	top, _ := m.handleScrollKey("home") // reach the top without fetching (content on screen)
	got, cmd := top.handleScrollKey("up")
	if cmd == nil {
		t.Fatal("up at the top with more history must dispatch an older-page fetch")
	}
	if !got.histories["general"].loading {
		t.Fatal("fetch must latch loading")
	}
}

func TestHandleScrollKeyNoFetchWhenNotAtTop(t *testing.T) {
	m := scrollModel(t, 100)
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "1"}}
	_, cmd := m.handleScrollKey("up") // one line up from the bottom
	if cmd != nil {
		t.Fatal("up away from the top must not fetch")
	}
}

func TestFetchOlderGuards(t *testing.T) {
	base := scrollModel(t, 10)
	base.scrollOffset = 0
	cases := map[string]roomHistory{
		"loading":   {loading: true, next: "1"},
		"exhausted": {exhausted: true, next: "1"},
		"no-cursor": {next: ""},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			m := base
			m.histories = map[string]roomHistory{"general": h}
			if _, cmd := m.fetchOlder(); cmd != nil {
				t.Fatalf("%s must not dispatch a fetch", name)
			}
		})
	}
}

func TestInboundFrameHoldsScrolledView(t *testing.T) {
	m := scrollModel(t, 50)
	m.scrollOffset = 5 // scrolled up, not at either extreme
	// A newer, same-day frame appends exactly one line (no date separator).
	newest := scrollBase.Add(time.Hour).UnixMilli()
	next, _ := m.Update(chatFrame(m, "message", messageFrameJSON("general", "bob", "newest", 999, newest)))
	got := next.(Model)
	// The appended line slides the offset up by one so the same content stays
	// anchored instead of the view jumping to the bottom.
	if got.scrollOffset != 6 {
		t.Fatalf("appended frame should hold the view (offset 6), got %d", got.scrollOffset)
	}
}

func TestPinnedViewFollowsNewFrames(t *testing.T) {
	m := scrollModel(t, 50)
	m.scrollOffset = 0 // pinned to the bottom
	newest := scrollBase.Add(time.Hour).UnixMilli()
	next, _ := m.Update(chatFrame(m, "message", messageFrameJSON("general", "bob", "newest", 999, newest)))
	if got := next.(Model).scrollOffset; got != 0 {
		t.Fatalf("a pinned view must stay pinned and follow new frames, got %d", got)
	}
}

func TestBackfillPrependHoldsScrolledView(t *testing.T) {
	// The bucket uses a high id base (100..149) so the older page (90..94)
	// prepends strictly above it. Event ids must be positive, hence the base.
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	msgs := make([]mainview.Message, 0, 50)
	for i := int64(0); i < 50; i++ {
		msgs = append(msgs, mainview.Message{ID: timeline.EventID(100 + i), Kind: mainview.KindChat, At: scrollBase.Add(time.Duration(i) * time.Second)})
	}
	m.messages["general"] = msgs // ids 100..149
	m.scrollOffset = 5
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "100"}}

	older := make([]api.TimelineEvent, 0, 5)
	for id := int64(90); id <= 94; id++ {
		older = append(older, api.TimelineEvent{EventID: timeline.EventID(id), Kind: api.TimelineMessage, Person: api.UserRef{ID: "u"}, At: scrollBase.Add(-time.Hour)})
	}
	next, _ := m.Update(api.RoomEventsLoaded{RoomID: "general", Events: older, Next: "89", Before: "100", Generation: 1})
	if got := next.(Model).scrollOffset; got != 5 {
		t.Fatalf("a prepended older page must hold the offset at 5, got %d", got)
	}
}

func TestActivationResetsScrollOffset(t *testing.T) {
	m := scrollModel(t, 50)
	m.scrollOffset = 7
	next, _ := m.Update(api.RoomJoined{RoomID: "r2", RoomName: "Room Two", Generation: 1})
	if got := next.(Model).scrollOffset; got != 0 {
		t.Fatalf("activating a room must reset the scroll offset, got %d", got)
	}
}
