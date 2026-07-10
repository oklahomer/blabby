package roomsearch

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/oklahomer/blabby/cmd/client/internal/api"
)

// recorder captures the most recent submit + load invocations so the
// tests can assert which Cmds the modal would dispatch in each phase
// without actually opening sockets.
type recorder struct {
	submittedRoomID   string
	submittedRoomName string
	loadCount         int
	lastQuery         api.RoomQuery
}

func (r *recorder) submit(roomID, roomName string) tea.Cmd {
	r.submittedRoomID = roomID
	r.submittedRoomName = roomName
	return func() tea.Msg { return "submit-sentinel" }
}

func (r *recorder) load(query api.RoomQuery) tea.Cmd {
	r.loadCount++
	r.lastQuery = query
	return func() tea.Msg { return "load-sentinel" }
}

// asModel unwraps the (modal.Modal, tea.Cmd) tuple to a concrete
// Model for assertions. Fails the test if the returned modal is not
// the search modal type — when the modal dismisses it returns nil
// and tests must opt into that explicitly.
func asModel(t *testing.T, m any) Model {
	t.Helper()
	got, ok := m.(Model)
	if !ok {
		t.Fatalf("expected roomsearch.Model, got %T", m)
	}
	return got
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestRoomsLoadedTransitionsToIdle(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")

	next, _ := m.Update(api.RoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
	}})
	got := asModel(t, next)

	if got.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle", got.phase)
	}
	if len(got.all) != 1 || got.all[0].ID != "general" {
		t.Fatalf("catalogue not stored: %#v", got.all)
	}
	if got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", got.cursor)
	}
	if got.headline != "" {
		t.Fatalf("headline should be cleared, got %q", got.headline)
	}
}

func TestRoomsLoadFailedShowsHeadline(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")

	next, _ := m.Update(api.RoomsLoadFailed{
		Status: "SERVICE_UNAVAILABLE", Message: "down", HTTPStatus: 503,
	})
	got := asModel(t, next)
	if got.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle", got.phase)
	}
	if !strings.Contains(got.headline, "Server unavailable") {
		t.Fatalf("headline = %q, want server-unavailable mapping", got.headline)
	}
}

func TestRoomsLoadFailedTransportErrorMentionsServer(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")

	next, _ := m.Update(api.RoomsLoadFailed{Message: "connection refused"})
	got := asModel(t, next)
	if !strings.Contains(got.headline, "Cannot reach server") {
		t.Fatalf("headline = %q, want server-unreachable message", got.headline)
	}
	if !strings.Contains(got.detail, "connection refused") {
		t.Fatalf("detail = %q, want transport reason", got.detail)
	}
}

func TestEnterSubmitsForCurrentCursorRow(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
		{ID: "random", Name: "Random"},
	}})

	next, _ := m.Update(keyMsg("down"))
	m = asModel(t, next)
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.cursor)
	}

	next, cmd := m.Update(keyMsg("enter"))
	m = asModel(t, next)
	if cmd == nil {
		t.Fatal("expected submit Cmd")
	}
	cmd() // fire submit so the recorder fills in
	if r.submittedRoomID != "random" || r.submittedRoomName != "Random" {
		t.Fatalf("submit got (%q, %q)", r.submittedRoomID, r.submittedRoomName)
	}
	if m.phase != phaseJoining {
		t.Fatalf("phase = %v, want phaseJoining", m.phase)
	}
	if m.joiningName != "Random" {
		t.Fatalf("joiningName = %q, want Random", m.joiningName)
	}
}

func TestEnterOnEmptyListIsNoOp(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: nil})

	next, cmd := m.Update(keyMsg("enter"))
	if cmd != nil {
		t.Fatal("expected no submit on empty list")
	}
	if asModel(t, next).phase == phaseJoining {
		t.Fatal("must not enter phaseJoining on empty list")
	}
}

func TestFilterTypingNarrowsListAndClampsCursor(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
		{ID: "random", Name: "Random"},
	}})

	// Move cursor onto the second row, then type a filter that only
	// matches the first. Cursor must clamp to 0 because there is now
	// only one visible row.
	next, _ := m.Update(keyMsg("down"))
	m = asModel(t, next)
	for _, r := range "gen" {
		nx, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = asModel(t, nx)
	}
	visible := Visible(m.all, m.filter.Value())
	if len(visible) != 1 {
		t.Fatalf("expected 1 visible after typing gen, got %d", len(visible))
	}
	if m.cursor != 0 {
		t.Fatalf("cursor not clamped: %d", m.cursor)
	}
}

func TestEscDismissesModal(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	next, cmd := m.Update(keyMsg("esc"))
	if next != nil {
		t.Fatalf("expected nil modal (dismissed), got %T", next)
	}
	if cmd != nil {
		t.Fatal("esc should not fire any Cmd")
	}
}

func TestKeysSuppressedWhileJoining(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
	}})
	next, _ := m.Update(keyMsg("enter"))
	m = asModel(t, next)
	if m.phase != phaseJoining {
		t.Fatalf("setup failed: phase=%v", m.phase)
	}

	// esc no longer dismisses while joining; cursor keys are absorbed.
	next, cmd := m.Update(keyMsg("esc"))
	if next == nil {
		t.Fatal("esc must not dismiss while joining")
	}
	if cmd != nil {
		t.Fatal("esc must not produce a Cmd while joining")
	}
}

func TestRoomJoinFailedRestoresIdleAndShowsHeadline(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
		{ID: "random", Name: "Random"},
	}})
	next, _ := m.Update(keyMsg("enter"))
	m = asModel(t, next)
	if m.phase != phaseJoining {
		t.Fatalf("setup failed: phase=%v", m.phase)
	}

	next, _ = m.Update(api.RoomJoinFailed{
		RoomID: "random", Status: "ROOM_ALREADY_MEMBER", Message: "already a member", HTTPStatus: 409,
	})
	got := asModel(t, next)
	if got.phase != phaseIdle {
		t.Fatalf("phase = %v, want phaseIdle", got.phase)
	}
	if !strings.Contains(got.headline, "Already joined this room") {
		t.Fatalf("headline = %q, want already-joined mapping", got.headline)
	}
}

func TestRoomJoinedDismissesModal(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	next, _ := m.Update(api.RoomJoined{RoomID: "general", RoomName: "General"})
	if next != nil {
		t.Fatalf("expected nil modal after RoomJoined, got %T", next)
	}
}

// TestVimKeysForwardedToFilter pins AC #11's contract that 'j' and 'k'
// are treated as printable characters by the filter input — they must
// NOT navigate the cursor. Cursor stepping is reserved for the arrow
// keys so the filter remains the focused field.
func TestVimKeysForwardedToFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
	}{
		{"j flows into filter", "j"},
		{"k flows into filter", "k"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &recorder{}
			m := New(r.submit, r.load, "http://localhost:8080")
			m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
				{ID: "general", Name: "General"},
				{ID: "random", Name: "Random"},
			}})
			startCursor := m.cursor

			next, _ := m.Update(keyMsg(tc.key))
			got := asModel(t, next)

			if got.cursor != startCursor {
				t.Errorf("cursor moved to %d (want %d) — vim key wrongly consumed by navigation", got.cursor, startCursor)
			}
			if got.filter.Value() != tc.key {
				t.Errorf("filter = %q, want %q (key not forwarded to textinput)", got.filter.Value(), tc.key)
			}
		})
	}
}

func TestInitDispatchesLoad(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("expected non-nil Init Cmd")
	}
	cmd() // tea.Batch returns a sequence msg — the recorder is enough to count the load call
	if r.loadCount == 0 {
		t.Fatal("expected loader to be invoked from Init")
	}
}

func TestViewIncludesTitleAndFooter(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	out := m.View(80, 24)
	if !strings.Contains(out, "Search rooms") {
		t.Errorf("title missing:\n%s", out)
	}
	if !strings.Contains(out, "esc: cancel") {
		t.Errorf("idle footer missing:\n%s", out)
	}
}

func TestViewRendersJoiningFooter(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
	}})
	next, _ := m.Update(keyMsg("enter"))
	m = asModel(t, next)
	out := m.View(80, 24)
	if !strings.Contains(out, "Joining General…") {
		t.Errorf("joining footer missing:\n%s", out)
	}
}

func TestViewLoadingPhaseShowsHint(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	out := m.View(80, 24)
	if !strings.Contains(out, "(loading…)") {
		t.Errorf("expected loading hint:\n%s", out)
	}
}

func TestViewEmptyFilterShowsNoRoomsAvailable(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: nil})
	out := m.View(80, 24)
	if !strings.Contains(out, "(no rooms available · ctrl+n: create room)") {
		t.Errorf("expected no-rooms hint:\n%s", out)
	}
}

func TestViewNonMatchingFilterShowsNoMatches(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{
		{ID: "general", Name: "General"},
	}})
	for _, ch := range "zzz" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = asModel(t, next)
	}
	out := m.View(80, 24)
	if !strings.Contains(out, "(no rooms match this filter · ctrl+n: create room)") {
		t.Errorf("expected no-matches hint:\n%s", out)
	}
}

// updateAs forwards a message and returns the updated Model. Encodes
// the "next" model unwrap so test bodies stay one-line.
func updateAs(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return asModel(t, next), cmd
}

func TestTypingArmsDebounceAndFiresServerQuery(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{{ID: "general", Name: "General"}}})
	loadsBefore := r.loadCount

	var tickCmd tea.Cmd
	for _, ch := range "gen" {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = asModel(t, next)
		tickCmd = cmd
	}
	if tickCmd == nil {
		t.Fatal("expected a debounce cmd from a fragment-changing keystroke")
	}
	// The debounce cmd is a tea.Tick batch; we simulate its firing by feeding
	// the searchTick for the latest sequence straight into Update.
	next, cmd := m.Update(searchTick{seq: m.debounceSeq})
	m = asModel(t, next)
	if cmd == nil {
		t.Fatal("expected the matching tick to dispatch a server query")
	}
	cmd()
	if r.loadCount != loadsBefore+1 || r.lastQuery.Query != "gen" || r.lastQuery.After != "" {
		t.Fatalf("load = %d query=%+v, want one first-page query for gen", r.loadCount-loadsBefore, r.lastQuery)
	}
}

func TestStaleDebounceTickIsDropped(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: nil})
	for _, ch := range "gen" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = asModel(t, next)
	}

	if _, cmd := m.Update(searchTick{seq: m.debounceSeq - 1}); cmd != nil {
		t.Fatal("a tick from an earlier keystroke must not query")
	}
}

func TestStaleRoomsLoadedIsDropped(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{{ID: "general", Name: "General"}}})
	for _, ch := range "xyz" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = asModel(t, next)
	}

	// A page for the fragment "gen" arrives after the user typed "xyz".
	next, _ := m.Update(api.RoomsLoaded{Rooms: []api.Room{{ID: "stale", Name: "Stale"}}, Query: "gen"})
	got := asModel(t, next)
	if len(got.all) != 1 || got.all[0].ID != "general" {
		t.Fatalf("stale page replaced the list: %#v", got.all)
	}
}

func TestStaleRoomsLoadFailedIsDropped(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: []api.Room{{ID: "general", Name: "General"}}})
	for _, ch := range "xyz" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = asModel(t, next)
	}

	// A failure for the fragment "gen" arrives after the user typed "xyz".
	next, _ := m.Update(api.RoomsLoadFailed{
		Query: "gen", Status: "SERVICE_UNAVAILABLE", Message: "down", HTTPStatus: 503,
	})
	got := asModel(t, next)
	if len(got.all) != 1 || got.all[0].ID != "general" {
		t.Fatalf("stale failure replaced the list: %#v", got.all)
	}
	if got.headline != "" {
		t.Fatalf("stale failure rendered headline %q", got.headline)
	}
}

func TestMoreRowFetchesAndAppendsNextPage(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "general", Name: "General"}},
		Next:  "RG000000004",
	})

	// down moves past the only room onto the more row; enter fetches.
	next, _ := m.Update(keyMsg("down"))
	m = asModel(t, next)
	if !m.onMoreRow(Visible(m.all, m.filter.Value())) {
		t.Fatalf("cursor = %d, want the more row", m.cursor)
	}
	next, cmd := m.Update(keyMsg("enter"))
	m = asModel(t, next)
	if cmd == nil {
		t.Fatal("expected a next-page load from the more row")
	}
	cmd()
	if r.lastQuery.After != "RG000000004" {
		t.Fatalf("After = %q, want the server cursor", r.lastQuery.After)
	}
	if !m.loadingMore {
		t.Fatal("expected loadingMore while the page request is in flight")
	}

	// The appended page joins the list and the new cursor replaces next.
	next, _ = m.Update(api.RoomsLoaded{
		Rooms: []api.Room{{ID: "general2", Name: "General 2"}},
		Next:  "", Query: "", After: "RG000000004",
	})
	got := asModel(t, next)
	if len(got.all) != 2 || got.all[1].ID != "general2" {
		t.Fatalf("page not appended: %#v", got.all)
	}
	if got.next != "" || got.loadingMore {
		t.Fatalf("next=%q loadingMore=%t, want exhausted and settled", got.next, got.loadingMore)
	}
}

func TestMoreRowFailureSettlesLoadingAndShowsError(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "general", Name: "General"}},
		Next:  "cursor-1",
	})
	next, _ := m.Update(keyMsg("down"))
	m = asModel(t, next)
	next, _ = m.Update(keyMsg("enter"))
	m = asModel(t, next)
	if !m.loadingMore {
		t.Fatal("setup: expected loadingMore")
	}

	next, _ = m.Update(api.RoomsLoadFailed{
		After: "cursor-1", Status: "SERVICE_UNAVAILABLE", Message: "down", HTTPStatus: 503,
	})
	got := asModel(t, next)
	if got.loadingMore {
		t.Fatal("loadingMore not settled after current append failure")
	}
	if !strings.Contains(got.headline, "Server unavailable") {
		t.Fatalf("headline = %q, want current append failure rendered", got.headline)
	}
}

func TestCtrlNEmitsCreateRoomRequested(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: nil})

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if _, ok := next.(Model); !ok {
		t.Fatalf("expected the modal to stay until the root swaps it, got %T", next)
	}
	if cmd == nil {
		t.Fatal("expected a CreateRoomRequested cmd")
	}
	if _, ok := cmd().(CreateRoomRequested); !ok {
		t.Fatal("expected CreateRoomRequested message")
	}
}

func TestPageAndEdgeNavigation(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	rooms := make([]api.Room, 25)
	for i := range rooms {
		rooms[i] = api.Room{ID: string(rune('a' + i)), Name: "Room"}
	}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{Rooms: rooms})

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = asModel(t, next)
	if m.cursor != pageStep {
		t.Fatalf("pgdn cursor = %d, want %d", m.cursor, pageStep)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = asModel(t, next)
	if m.cursor != len(rooms)-1 {
		t.Fatalf("end cursor = %d, want %d", m.cursor, len(rooms)-1)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = asModel(t, next)
	if m.cursor != len(rooms)-1-pageStep {
		t.Fatalf("pgup cursor = %d, want %d", m.cursor, len(rooms)-1-pageStep)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyHome})
	m = asModel(t, next)
	if m.cursor != 0 {
		t.Fatalf("home cursor = %d, want 0", m.cursor)
	}
}

func TestAppendRacingReplaceIsDropped(t *testing.T) {
	t.Parallel()
	// The user pages the more row, then a debounced same-query replace lands
	// with different content before the append arrives. The append no longer
	// continues the listing on screen and must be dropped whole.
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "a", Name: "A"}},
		Next:  "cursor-1",
	})
	next, _ := m.Update(keyMsg("down")) // onto the more row
	m = asModel(t, next)
	next, _ = m.Update(keyMsg("enter")) // append for cursor-1 now in flight
	m = asModel(t, next)

	// The replace lands first: fresh first page, cursor moved.
	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "a2", Name: "A2"}},
		Next:  "cursor-9",
	})

	// The stale append arrives for the old cursor.
	next, _ = m.Update(api.RoomsLoaded{
		Rooms: []api.Room{{ID: "b", Name: "B"}},
		Next:  "cursor-2", After: "cursor-1",
	})
	got := asModel(t, next)
	if len(got.all) != 1 || got.all[0].ID != "a2" {
		t.Fatalf("stale append corrupted the list: %#v", got.all)
	}
	if got.next != "cursor-9" {
		t.Fatalf("next = %q, want the fresh listing's cursor", got.next)
	}
	if got.loadingMore {
		t.Fatal("loadingMore must settle when the stale append is dropped")
	}
}

func TestAppendFailureRacingReplaceIsDropped(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "a", Name: "A"}},
		Next:  "cursor-1",
	})
	next, _ := m.Update(keyMsg("down"))
	m = asModel(t, next)
	next, _ = m.Update(keyMsg("enter"))
	m = asModel(t, next)

	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "a2", Name: "A2"}},
		Next:  "cursor-9",
	})

	next, _ = m.Update(api.RoomsLoadFailed{
		After: "cursor-1", Status: "SERVICE_UNAVAILABLE", Message: "down", HTTPStatus: 503,
	})
	got := asModel(t, next)
	if len(got.all) != 1 || got.all[0].ID != "a2" {
		t.Fatalf("stale append failure corrupted the list: %#v", got.all)
	}
	if got.next != "cursor-9" {
		t.Fatalf("next = %q, want the fresh listing's cursor", got.next)
	}
	if got.headline != "" {
		t.Fatalf("stale append failure rendered headline %q", got.headline)
	}
	if got.loadingMore {
		t.Fatal("loadingMore must settle when the stale append failure is dropped")
	}
}

func TestMoreRowIgnoredWhileFilterOutrunsListing(t *testing.T) {
	t.Parallel()
	// The user typed past the loaded fragment; a debounced replace is on its
	// way, so paging the outgoing listing is refused.
	r := &recorder{}
	m := New(r.submit, r.load, "http://localhost:8080")
	m, _ = updateAs(t, m, api.RoomsLoaded{
		Rooms: []api.Room{{ID: "alpha", Name: "Alpha"}},
		Next:  "cursor-1",
	})
	for _, ch := range "alp" {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		m = asModel(t, next)
	}
	next, _ := m.Update(keyMsg("down"))
	m = asModel(t, next)
	if !m.onMoreRow(Visible(m.all, m.filter.Value())) {
		t.Fatalf("setup: cursor = %d, want the more row", m.cursor)
	}
	loadsBefore := r.loadCount
	next, cmd := m.Update(keyMsg("enter"))
	m = asModel(t, next)
	if cmd != nil || r.loadCount != loadsBefore || m.loadingMore {
		t.Fatal("paging must be refused while the filter has outrun the listing")
	}
}
