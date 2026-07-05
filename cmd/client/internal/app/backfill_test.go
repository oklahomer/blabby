package app

import (
	"net/http"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
	"github.com/oklahomer/blabby/cmd/client/internal/login"
	"github.com/oklahomer/blabby/cmd/client/internal/panes/mainview"
)

func chatMsg(id int64, text string) api.TimelineEvent {
	return api.TimelineEvent{
		EventID: id,
		Kind:    api.TimelineMessage,
		Person:  api.UserRef{ID: "u-x", Name: "X"},
		Text:    text,
		At:      time.UnixMilli(1_700_000_000_000),
	}
}

func TestBeginBackfillLatchesSingleFlight(t *testing.T) {
	m := chatReadyModel(t)
	m, cmd := m.beginBackfill("general")
	if cmd == nil {
		t.Fatal("first beginBackfill must dispatch a fetch")
	}
	if !m.histories["general"].loading {
		t.Fatal("first beginBackfill must latch loading")
	}
	m2, cmd2 := m.beginBackfill("general")
	if cmd2 != nil {
		t.Fatal("a second beginBackfill while loading must not dispatch")
	}
	if !m2.histories["general"].loading {
		t.Fatal("loading latch must remain set")
	}
}

func TestActivationDispatchesBackfill(t *testing.T) {
	m := chatReadyModel(t)
	m.focus = focusRooms
	m.roomsState.JoinedIDs = []string{"general"}
	m.roomsState.NameForID = map[string]string{"general": "general"}
	m.activeRoomID = ""

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if cmd == nil {
		t.Fatal("activating a room must dispatch a backfill fetch")
	}
	if got.activeRoomID != "general" {
		t.Fatalf("activeRoomID = %q, want general", got.activeRoomID)
	}
	if !got.histories["general"].loading {
		t.Fatal("activation must latch the room's loading flag")
	}
}

func TestRoomEventsLoadedMergesAndDedups(t *testing.T) {
	m := chatReadyModel(t)
	// A live frame already placed id 100 in the bucket.
	m.messages["general"] = []mainview.Message{{ID: 100, Kind: mainview.KindChat, Sender: "live", Text: "hello100"}}

	next, cmd := m.Update(api.RoomEventsLoaded{
		RoomID:     "general",
		Events:     []api.TimelineEvent{chatMsg(100, "dupe"), chatMsg(99, "old")},
		Next:       "50",
		Before:     "",
		Generation: 1,
	})
	got := next.(Model)
	if cmd != nil {
		t.Fatal("a loaded page must not dispatch a cmd")
	}
	bucket := got.messages["general"]
	if len(bucket) != 2 {
		t.Fatalf("expected 2 entries after dedup, got %d: %#v", len(bucket), bucket)
	}
	if bucket[0].ID != 99 || bucket[1].ID != 100 {
		t.Fatalf("entries not ordered by id: %#v", bucket)
	}
	// The duplicate id kept the original live content, not the backfilled one.
	if bucket[1].Text != "hello100" {
		t.Fatalf("dedup replaced the live entry: %#v", bucket[1])
	}
	h := got.histories["general"]
	if h.loading || !h.fetched || h.next != "50" || h.exhausted {
		t.Fatalf("history state wrong after load: %#v", h)
	}
}

func TestRoomEventsLoadedNewestPageDoesNotClobberDeeperCursor(t *testing.T) {
	m := chatReadyModel(t)
	// The room has already been fetched and a scroll-up advanced the cursor.
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "50"}}

	next, _ := m.Update(api.RoomEventsLoaded{
		RoomID:     "general",
		Events:     []api.TimelineEvent{chatMsg(200, "newest")},
		Next:       "190", // the page-1 cursor — must NOT overwrite the deeper "50"
		Before:     "",
		Generation: 1,
	})
	if got := next.(Model).histories["general"].next; got != "50" {
		t.Fatalf("newest-page refresh rewound the cursor to %q, want the deeper 50", got)
	}
}

func TestRoomEventsLoadedOlderPageAdvancesCursor(t *testing.T) {
	m := chatReadyModel(t)
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "90"}}

	next, _ := m.Update(api.RoomEventsLoaded{
		RoomID:     "general",
		Events:     []api.TimelineEvent{chatMsg(80, "older")},
		Next:       "40",
		Before:     "90",
		Generation: 1,
	})
	if got := next.(Model).histories["general"].next; got != "40" {
		t.Fatalf("older page did not advance the cursor: got %q, want 40", got)
	}
}

func TestRoomEventsLoadedNullNextExhausts(t *testing.T) {
	m := chatReadyModel(t)
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "90"}}

	next, _ := m.Update(api.RoomEventsLoaded{
		RoomID:     "general",
		Events:     []api.TimelineEvent{chatMsg(80, "last")},
		Next:       "",
		Before:     "90",
		Generation: 1,
	})
	h := next.(Model).histories["general"]
	if !h.exhausted || h.next != "" {
		t.Fatalf("null next should exhaust history: %#v", h)
	}
}

func TestRoomEventsLoadedStaleGenerationDropped(t *testing.T) {
	m := chatReadyModel(t)
	m.sessionGeneration = 2
	m.messages["general"] = []mainview.Message{{ID: 5, Kind: mainview.KindChat, Text: "keep"}}

	next, _ := m.Update(api.RoomEventsLoaded{
		RoomID:     "general",
		Events:     []api.TimelineEvent{chatMsg(1, "stale")},
		Generation: 1,
	})
	got := next.(Model)
	if len(got.messages["general"]) != 1 || got.messages["general"][0].Text != "keep" {
		t.Fatalf("stale page mutated the bucket: %#v", got.messages["general"])
	}
}

func TestRoomEventsLoadFailedShowsErrorForActiveRoom(t *testing.T) {
	m := chatReadyModel(t)
	m.histories = map[string]roomHistory{"general": {loading: true, fetched: true}}

	next, _ := m.Update(api.RoomEventsLoadFailed{
		RoomID:     "general",
		HTTPStatus: http.StatusServiceUnavailable,
		Status:     "SERVICE_UNAVAILABLE",
		Message:    "down",
		Generation: 1,
	})
	got := next.(Model)
	if got.histories["general"].loading {
		t.Fatal("failure must clear the loading latch")
	}
	if got.mainError != "Server unavailable — please try again" {
		t.Fatalf("mainError = %q", got.mainError)
	}
	if got.conn == nil {
		t.Fatal("a non-401 backfill failure must not tear down the session")
	}
}

func TestRoomEventsLoadFailedInactiveRoomIsSilent(t *testing.T) {
	m := chatReadyModel(t) // active room is "general"
	m.histories = map[string]roomHistory{"other": {loading: true}}

	next, _ := m.Update(api.RoomEventsLoadFailed{
		RoomID:     "other",
		HTTPStatus: http.StatusServiceUnavailable,
		Status:     "SERVICE_UNAVAILABLE",
		Message:    "down",
		Generation: 1,
	})
	got := next.(Model)
	if got.mainError != "" {
		t.Fatalf("a failure for a non-active room must not surface an error: %q", got.mainError)
	}
	if got.histories["other"].loading {
		t.Fatal("failure must clear the loading latch even for a non-active room")
	}
}

func TestRoomEventsLoadFailedUnauthorizedExpiresSession(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.histories = map[string]roomHistory{"general": {loading: true}}

	next, cmd := m.Update(api.RoomEventsLoadFailed{
		RoomID:     "general",
		HTTPStatus: http.StatusUnauthorized,
		Status:     "AUTH_EXPIRED_TOKEN",
		Generation: 1,
	})
	got := next.(Model)
	if got.token != "" {
		t.Fatalf("401 backfill failure did not discard the token: %q", got.token)
	}
	if _, ok := got.modal.(login.Model); !ok {
		t.Fatalf("expected the login modal reopened, got %T", got.modal)
	}
	if got.histories != nil {
		t.Fatalf("session expiry must clear histories: %#v", got.histories)
	}
	if cmd == nil {
		t.Fatal("expected the login modal Init cmd")
	}
}

func TestTrimHealsBackfillCursor(t *testing.T) {
	m := chatReadyModel(t)
	// Fill past the cap so the oldest entries are trimmed; the cursor must
	// heal to the surviving oldest so scroll-up re-fetches the trimmed span.
	for i := int64(1); i <= eventBucketCap+1; i++ {
		m = m.appendEvent("general", mainview.Message{ID: i, Kind: mainview.KindChat})
	}
	if len(m.messages["general"]) != eventBucketCap {
		t.Fatalf("bucket not capped: len = %d", len(m.messages["general"]))
	}
	h := m.histories["general"]
	// Oldest surviving id is 2 (id 1 was trimmed).
	if h.next != "2" || h.exhausted || !h.fetched {
		t.Fatalf("trim did not heal the cursor: %#v", h)
	}
}

func TestWSDisconnectedClearsHistories(t *testing.T) {
	m := chatReadyModel(t)
	m.width, m.height = 100, 30
	m.histories = map[string]roomHistory{"general": {fetched: true, next: "10"}}

	next, _ := m.Update(api.WSDisconnected{Generation: 1})
	if got := next.(Model).histories; got != nil {
		t.Fatalf("disconnect must clear histories, got %#v", got)
	}
}
