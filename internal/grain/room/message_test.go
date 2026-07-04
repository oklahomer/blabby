package room_test

import (
	"context"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/id"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"
)

// fakeMessageStore is a spy room.MessageStore for unit tests: it records every
// RecordMessage call, returns a configurable event, and can be made to fail to
// exercise the grain's fail-closed path.
type fakeMessageStore struct {
	event room.MessageEvent
	err   error
	calls []messageCall
}

// messageCall records one RecordMessage invocation.
type messageCall struct {
	author id.UserID
	text   string
}

func (f *fakeMessageStore) RecordMessage(_ context.Context, _ id.RoomID, author id.UserID, text string) (room.MessageEvent, error) {
	f.calls = append(f.calls, messageCall{author: author, text: text})
	if f.err != nil {
		return room.MessageEvent{}, f.err
	}
	return f.event, nil
}

// messageEventFixture is a non-zero MessageEvent the spy returns so tests can
// assert the durable timestamp travels onto the response and fan-out.
func messageEventFixture(t *testing.T) room.MessageEvent {
	t.Helper()
	eid, err := id.NewEventID(987654322)
	if err != nil {
		t.Fatalf("event id: %v", err)
	}
	return room.MessageEvent{ID: eid, OccurredAt: time.UnixMilli(1_700_000_000_500)}
}

// newMessageGrain builds a loaded grain wired with store and a recording
// notifier. Membership stays memory-only: message persistence is the seam under
// test.
func newMessageGrain(t *testing.T, store room.MessageStore) (*room.Grain, *fakeNotifier) {
	t.Helper()
	g := &room.Grain{}
	notifier := &fakeNotifier{}
	g.SetNotifier(notifier)
	g.SetLoader(seededLoader(roomRef(t, testRoomID, domain.RoomStatusActive)))
	g.SetMessageStore(store)
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))
	return g, notifier
}

func TestGrain_PostMessage_PersistsAndUsesEventTimestamp(t *testing.T) {
	evt := messageEventFixture(t)
	store := &fakeMessageStore{event: evt}
	g, notifier := newMessageGrain(t, store)
	mustJoin(t, g, "1")
	mustJoin(t, g, "2")
	notifier.forwardCalls = nil

	resp, err := g.PostMessage(graintest.NewPostMessageRequestNamed("1", "Alice", "hello"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("expected success, got error: %+v", resp.GetError())
	}

	want := []messageCall{{author: mustUserID(t, "1"), text: "hello"}}
	if len(store.calls) != 1 || store.calls[0] != want[0] {
		t.Errorf("RecordMessage calls: got %v, want %v", store.calls, want)
	}
	// The durable event's occurred_at is the message's timestamp everywhere:
	// the response and every fan-out copy, so live frames agree with the
	// timeline.
	if got := resp.GetTimestamp().AsTime(); !got.Equal(evt.OccurredAt) {
		t.Errorf("response timestamp = %v, want the event's occurred_at %v", got, evt.OccurredAt)
	}
	if len(notifier.forwardCalls) != 2 {
		t.Fatalf("forwardCalls: got %d, want 2", len(notifier.forwardCalls))
	}
	for i, c := range notifier.forwardCalls {
		if !c.Timestamp.Equal(evt.OccurredAt) {
			t.Errorf("forwardCalls[%d].Timestamp = %v, want %v", i, c.Timestamp, evt.OccurredAt)
		}
	}
	if got := g.RecentMessageCount(); got != 1 {
		t.Errorf("RecentMessageCount: got %d, want 1", got)
	}
}

func TestGrain_PostMessage_WriteFailureFailsClosed(t *testing.T) {
	store := &fakeMessageStore{err: errFake("db write failed")}
	g, notifier := newMessageGrain(t, store)
	mustJoin(t, g, "1")
	notifier.forwardCalls = nil

	resp, err := g.PostMessage(graintest.NewPostMessageRequest("1", "hello"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
	// Fail-closed: nothing fanned out, nothing cached — the message does not
	// exist anywhere if it is not durable.
	if len(notifier.forwardCalls) != 0 {
		t.Errorf("forwardCalls: got %d, want 0", len(notifier.forwardCalls))
	}
	if got := g.RecentMessageCount(); got != 0 {
		t.Errorf("RecentMessageCount: got %d, want 0", got)
	}
}
