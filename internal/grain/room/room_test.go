package room_test

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/id"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"
)

// testRoomID is the grain identity used across these tests. It must be a valid
// decimal RoomID because the grain parses its identity on activation.
const testRoomID = "4"

// stubRoomLoader is an in-memory RoomLoader for tests. A seeded id loads its
// ref; an unseeded id reports room.ErrRoomNotFound; a non-nil err is returned
// for every id (to exercise the transient-failure path).
type stubRoomLoader struct {
	rooms map[id.RoomID]domain.RoomRef
	err   error
}

func (s stubRoomLoader) LoadRoom(_ context.Context, roomID id.RoomID) (domain.RoomRef, error) {
	if s.err != nil {
		return domain.RoomRef{}, s.err
	}
	ref, ok := s.rooms[roomID]
	if !ok {
		return domain.RoomRef{}, room.ErrRoomNotFound
	}
	return ref, nil
}

// seededLoader returns a stubRoomLoader keyed by each ref's ID.
func seededLoader(refs ...domain.RoomRef) stubRoomLoader {
	m := make(map[id.RoomID]domain.RoomRef, len(refs))
	for _, r := range refs {
		m[r.ID] = r
	}
	return stubRoomLoader{rooms: m}
}

// stubRoomPublicCode is a valid bare 10-symbol Crockford public_code, so a
// RoomRef built here survives the User grain's boundary parse when the Join flow
// carries it end to end (fanout_integration_test).
const stubRoomPublicCode = "G000000004"

// roomRef builds a RoomRef for a decimal RoomID with the given status, including
// a valid public code so it round-trips through the Join response.
func roomRef(t *testing.T, raw string, status domain.RoomStatus) domain.RoomRef {
	t.Helper()
	rid, err := id.ParseRoomID(raw)
	if err != nil {
		t.Fatalf("roomRef(%q): %v", raw, err)
	}
	code, err := id.ParsePublicCode(stubRoomPublicCode)
	if err != nil {
		t.Fatalf("roomRef public code: %v", err)
	}
	return domain.RoomRef{ID: rid, PublicCode: code, Name: "Room " + raw, Status: status}
}

// activeRoomRef builds an active RoomRef for a decimal RoomID, so test files that
// only need a joinable room don't reference the domain status enum directly.
func activeRoomRef(t *testing.T, raw string) domain.RoomRef {
	t.Helper()
	return roomRef(t, raw, domain.RoomStatusActive)
}

// Compile-time guard that stubRoomLoader satisfies room.RoomLoader.
var _ room.RoomLoader = stubRoomLoader{}

// mustUserID is a test helper that constructs a typed id.UserID, failing
// the test on any structural error. Used to keep table-driven cases
// readable without sprinkling NewUserID calls everywhere.
func mustUserID(t *testing.T, raw string) id.UserID {
	t.Helper()
	u, err := id.ParseUserID(raw)
	if err != nil {
		t.Fatalf("mustUserID(%q): %v", raw, err)
	}
	return u
}

// fakeRoomCtx returns a fake grain context with kind="RoomGrain", matching
// what cluster.NewKind("RoomGrain", ...) produces in production. Handlers
// in this package now derive grain_type from ctx.Kind(), so tests have to
// populate it.
func fakeRoomCtx(identity string, opts ...graintest.FakeGrainContextOption) cluster.GrainContext {
	return graintest.NewFakeGrainContext(identity, append([]graintest.FakeGrainContextOption{graintest.WithKind("RoomGrain")}, opts...)...)
}

// fakeNotifier records every fan-out call, in order, for assertion.
type fakeNotifier struct {
	notifyCalls  []notifyCall
	notifyReqs   []*userpb.NotifyRoomEventRequest
	forwardCalls []forwardCall
	notifyErrFn  func(userID string) error
	forwardErrFn func(userID string) error
}

// userRef groups a user's id and display name the way the production proto
// (commonpb.UserRef) carries them, so the recorders assert the pair travels
// together through fan-out rather than as two loose strings.
type userRef struct {
	ID   string
	Name string
}

type notifyCall struct {
	UserID    string
	RoomID    string
	Subject   userRef
	EventType userpb.RoomEventType
}

type forwardCall struct {
	UserID    string
	RoomID    string
	Sender    userRef
	Text      string
	Timestamp time.Time
}

func (f *fakeNotifier) NotifyRoomEvent(userID id.UserID, req *userpb.NotifyRoomEventRequest) error {
	f.notifyCalls = append(f.notifyCalls, notifyCall{
		UserID:    userID.String(),
		RoomID:    req.GetRoom().GetRoomId(),
		Subject:   userRef{ID: req.GetUser().GetId(), Name: req.GetUser().GetName()},
		EventType: req.GetEventType(),
	})
	f.notifyReqs = append(f.notifyReqs, req)
	if f.notifyErrFn != nil {
		return f.notifyErrFn(userID.String())
	}
	return nil
}

func (f *fakeNotifier) ForwardMessage(userID id.UserID, req *userpb.ForwardMessageRequest) error {
	f.forwardCalls = append(f.forwardCalls, forwardCall{
		UserID:    userID.String(),
		RoomID:    req.GetRoom().GetRoomId(),
		Sender:    userRef{ID: req.GetSender().GetId(), Name: req.GetSender().GetName()},
		Text:      req.GetText(),
		Timestamp: req.GetTimestamp().AsTime(),
	})
	if f.forwardErrFn != nil {
		return f.forwardErrFn(userID.String())
	}
	return nil
}

// newGrain returns an initialized Grain wired with a fakeNotifier and a
// counter-based clock that ticks 1ms forward on every call, starting at
// epoch + 1001 ms. Returning the *time.Time lets callers peek at the
// last-issued timestamp if they need to.
func newGrain(t *testing.T) (*room.Grain, *fakeNotifier, *time.Time) {
	t.Helper()
	g := &room.Grain{}
	notifier := &fakeNotifier{}
	g.SetNotifier(notifier)
	g.SetLoader(seededLoader(roomRef(t, testRoomID, domain.RoomStatusActive)))
	clock := time.UnixMilli(1000)
	g.SetClock(func() time.Time {
		clock = clock.Add(time.Millisecond)
		return clock
	})
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))
	return g, notifier, &clock
}

// initGrain builds a grain wired with the given loader and activates it. Used by
// activation tests that need a non-default load outcome (absent/archived room).
func initGrain(t *testing.T, loader room.RoomLoader) *room.Grain {
	t.Helper()
	g := &room.Grain{}
	g.SetNotifier(&fakeNotifier{})
	g.SetLoader(loader)
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))
	return g
}

func TestGrain_Join(t *testing.T) {
	t.Run("success — empty room records member and fans out one JOINED event", func(t *testing.T) {
		g, notifier, _ := newGrain(t)

		resp, err := g.Join(graintest.NewJoinRequestNamed("1", "Alice"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}

		if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "1")}) {
			t.Errorf("Members: got %v, want [alice]", got)
		}
		if len(notifier.notifyCalls) != 1 {
			t.Fatalf("notifyCalls: got %d, want 1", len(notifier.notifyCalls))
		}
		c := notifier.notifyCalls[0]
		want := notifyCall{UserID: "1", RoomID: testRoomID, Subject: userRef{ID: "1", Name: "Alice"}, EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED}
		if c != want {
			t.Errorf("notifyCalls[0]: got %+v, want %+v", c, want)
		}
	})

	t.Run("success — fans out to N+1 members when joining a populated room", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		mustJoin(t, g, "2")
		notifier.notifyCalls = nil // reset before the third join

		resp, err := g.Join(graintest.NewJoinRequest("6"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}

		if len(notifier.notifyCalls) != 3 {
			t.Fatalf("notifyCalls: got %d, want 3 (alice, bob, carol)", len(notifier.notifyCalls))
		}
		gotRecipients := []string{
			notifier.notifyCalls[0].UserID,
			notifier.notifyCalls[1].UserID,
			notifier.notifyCalls[2].UserID,
		}
		// memberIDs() is sorted, so fan-out is deterministic.
		want := []string{"1", "2", "6"}
		if !reflect.DeepEqual(gotRecipients, want) {
			t.Errorf("recipients: got %v, want %v", gotRecipients, want)
		}
		for i, c := range notifier.notifyCalls {
			// The default-name helper sets name = id, so the subject is
			// carried through fan-out as {ID: carol, Name: carol}.
			if want := (userRef{ID: "6", Name: "6"}); c.Subject != want {
				t.Errorf("notifyCalls[%d].Subject: got %+v, want %+v", i, c.Subject, want)
			}
			if c.EventType != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED {
				t.Errorf("notifyCalls[%d].EventType: got %v, want JOINED", i, c.EventType)
			}
		}
	})

	t.Run("empty user_id returns 4001 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)

		resp, err := g.Join(graintest.NewJoinRequest(""), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(notifier.notifyCalls) != 0 {
			t.Errorf("notifyCalls: got %d, want 0", len(notifier.notifyCalls))
		}
	})

	t.Run("already-member returns 2002 with no fan-out and unchanged state", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		notifier.notifyCalls = nil

		resp, err := g.Join(graintest.NewJoinRequest("1"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2002, "ROOM_ALREADY_MEMBER")
		if len(notifier.notifyCalls) != 0 {
			t.Errorf("notifyCalls: got %d, want 0", len(notifier.notifyCalls))
		}
		if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "1")}) {
			t.Errorf("Members: got %v, want [alice]", got)
		}
	})
}

func TestGrain_Join_CarriesRoomRef(t *testing.T) {
	t.Run("success carries the cached room ref", func(t *testing.T) {
		g, _, _ := newGrain(t)

		resp, err := g.Join(graintest.NewJoinRequest("1"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		room := resp.GetRoom()
		if room == nil {
			t.Fatal("Join response missing room ref")
		}
		if room.GetRoomId() != testRoomID || room.GetPublicCode() != stubRoomPublicCode ||
			room.GetName() != "Room "+testRoomID || room.GetStatus() != "active" {
			t.Errorf("room ref = %+v, want id=%s code=%s name=Room %s active",
				room, testRoomID, stubRoomPublicCode, testRoomID)
		}
	})

	t.Run("already-member still carries the room ref", func(t *testing.T) {
		g, _, _ := newGrain(t)
		mustJoin(t, g, "1")

		resp, err := g.Join(graintest.NewJoinRequest("1"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2002, "ROOM_ALREADY_MEMBER")
		if resp.GetRoom() == nil {
			t.Error("already-member Join response must still carry the room ref")
		}
	})

	t.Run("room-not-found omits the room ref", func(t *testing.T) {
		g := initGrain(t, seededLoader()) // unseeded → unloaded grain

		resp, err := g.Join(graintest.NewJoinRequest("1"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2003, "ROOM_NOT_FOUND")
		if resp.GetRoom() != nil {
			t.Errorf("ROOM_NOT_FOUND Join response must omit the room ref, got %+v", resp.GetRoom())
		}
	})
}

func TestGrain_Leave(t *testing.T) {
	t.Run("success — fans out LEFT to pre-removal snapshot including leaver", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		mustJoin(t, g, "2")
		notifier.notifyCalls = nil

		resp, err := g.Leave(&roompb.LeaveRequest{UserId: "1"}, fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}

		if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "2")}) {
			t.Errorf("Members: got %v, want [bob]", got)
		}
		if len(notifier.notifyCalls) != 2 {
			t.Fatalf("notifyCalls: got %d, want 2 (alice, bob)", len(notifier.notifyCalls))
		}
		gotRecipients := []string{notifier.notifyCalls[0].UserID, notifier.notifyCalls[1].UserID}
		if !reflect.DeepEqual(gotRecipients, []string{"1", "2"}) {
			t.Errorf("recipients: got %v, want [alice bob]", gotRecipients)
		}
		for i, c := range notifier.notifyCalls {
			if c.Subject != (userRef{ID: "1", Name: "1"}) || c.EventType != userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT {
				t.Errorf("notifyCalls[%d]: got %+v, want subject={alice alice} eventType=LEFT", i, c)
			}
		}
	})

	t.Run("empty user_id returns 4001 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		notifier.notifyCalls = nil

		resp, err := g.Leave(&roompb.LeaveRequest{UserId: ""}, fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(notifier.notifyCalls) != 0 {
			t.Errorf("notifyCalls: got %d, want 0", len(notifier.notifyCalls))
		}
	})

	t.Run("non-member returns 2001 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)

		resp, err := g.Leave(&roompb.LeaveRequest{UserId: "1"}, fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2001, "ROOM_NOT_MEMBER")
		if len(notifier.notifyCalls) != 0 {
			t.Errorf("notifyCalls: got %d, want 0", len(notifier.notifyCalls))
		}
	})
}

func TestGrain_PostMessage(t *testing.T) {
	t.Run("success — forwards to every member including sender with assigned timestamp", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
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
		if resp.GetTimestamp() == nil {
			t.Errorf("Timestamp: got nil, want populated")
		}

		if len(notifier.forwardCalls) != 2 {
			t.Fatalf("forwardCalls: got %d, want 2", len(notifier.forwardCalls))
		}
		gotRecipients := []string{notifier.forwardCalls[0].UserID, notifier.forwardCalls[1].UserID}
		if !reflect.DeepEqual(gotRecipients, []string{"1", "2"}) {
			t.Errorf("recipients: got %v, want [alice bob]", gotRecipients)
		}
		respTime := resp.GetTimestamp().AsTime()
		for i, c := range notifier.forwardCalls {
			if c.Sender != (userRef{ID: "1", Name: "Alice"}) || c.Text != "hello" || !c.Timestamp.Equal(respTime) {
				t.Errorf("forwardCalls[%d]: got %+v, want sender={alice Alice} text=hello ts=%v", i, c, respTime)
			}
		}
		if got := g.RecentMessageCount(); got != 1 {
			t.Errorf("RecentMessageCount: got %d, want 1", got)
		}
	})

	t.Run("two posts assign monotonically increasing timestamps and persist in buffer", func(t *testing.T) {
		g, _, _ := newGrain(t)
		mustJoin(t, g, "1")

		resp1, _ := g.PostMessage(graintest.NewPostMessageRequest("1", "one"), fakeRoomCtx(testRoomID))
		resp2, _ := g.PostMessage(graintest.NewPostMessageRequest("1", "two"), fakeRoomCtx(testRoomID))

		ts1 := resp1.GetTimestamp().AsTime()
		ts2 := resp2.GetTimestamp().AsTime()
		if !ts2.After(ts1) {
			t.Errorf("expected ts2 > ts1, got ts1=%v ts2=%v", ts1, ts2)
		}
		if got := g.RecentMessageCount(); got != 2 {
			t.Errorf("RecentMessageCount: got %d, want 2", got)
		}
	})

	t.Run("empty user_id returns 4001 with no fan-out and no state mutation", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(graintest.NewPostMessageRequest("", "hi"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(notifier.forwardCalls) != 0 {
			t.Errorf("forwardCalls: got %d, want 0", len(notifier.forwardCalls))
		}
		if g.RecentMessageCount() != 0 {
			t.Errorf("RecentMessageCount: got %d, want 0", g.RecentMessageCount())
		}
	})

	t.Run("empty text returns 4002 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(graintest.NewPostMessageRequest("1", ""), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4002, "MISSING_FIELD")
		if len(notifier.forwardCalls) != 0 {
			t.Errorf("forwardCalls: got %d, want 0", len(notifier.forwardCalls))
		}
	})

	t.Run("whitespace-only text returns 4002 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "1")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(graintest.NewPostMessageRequest("1", "  \t\n"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4002, "MISSING_FIELD")
		if len(notifier.forwardCalls) != 0 {
			t.Errorf("forwardCalls: got %d, want 0", len(notifier.forwardCalls))
		}
	})

	t.Run("non-member sender returns 2001 with no fan-out and no state mutation", func(t *testing.T) {
		g, notifier, _ := newGrain(t)

		resp, err := g.PostMessage(graintest.NewPostMessageRequest("1", "hi"), fakeRoomCtx(testRoomID))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2001, "ROOM_NOT_MEMBER")
		if len(notifier.forwardCalls) != 0 {
			t.Errorf("forwardCalls: got %d, want 0", len(notifier.forwardCalls))
		}
		if g.RecentMessageCount() != 0 {
			t.Errorf("RecentMessageCount: got %d, want 0", g.RecentMessageCount())
		}
	})
}

func TestGrain_FanOutErrorIsLoggedNotFatal(t *testing.T) {
	g, notifier, _ := newGrain(t)
	mustJoin(t, g, "1")
	mustJoin(t, g, "2")
	notifier.forwardCalls = nil
	notifier.forwardErrFn = func(userID string) error {
		if userID == "2" {
			return errFake("downstream user grain unreachable")
		}
		return nil
	}

	resp, err := g.PostMessage(graintest.NewPostMessageRequest("1", "hello"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Errorf("expected success even when one fan-out fails (best-effort delivery), got error: %+v", resp.GetError())
	}
	if len(notifier.forwardCalls) != 2 {
		t.Errorf("forwardCalls: got %d, want 2 (loop must continue past error)", len(notifier.forwardCalls))
	}
}

func TestGrain_Init_DefaultsClockWhenAbsent(t *testing.T) {
	g := &room.Grain{}
	g.SetNotifier(&fakeNotifier{})
	g.SetLoader(seededLoader(roomRef(t, testRoomID, domain.RoomStatusActive)))
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))

	mustJoin(t, g, "1")

	resp, err := g.PostMessage(graintest.NewPostMessageRequest("1", "hi"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("PostMessage failed: %+v", resp.GetError())
	}
	if ts := resp.GetTimestamp(); ts == nil || ts.AsTime().IsZero() {
		t.Errorf("expected default clock to assign a non-zero timestamp, got %v", ts)
	}
}

func TestGrain_Activation_InvalidRoomRejectsCommands(t *testing.T) {
	cases := []struct {
		name   string
		loader room.RoomLoader
	}{
		{"absent room", seededLoader()},
		{"archived room", seededLoader(roomRef(t, testRoomID, domain.RoomStatusArchived))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := initGrain(t, tc.loader)

			joinResp, err := g.Join(graintest.NewJoinRequest("1"), fakeRoomCtx(testRoomID))
			if err != nil {
				t.Fatalf("Join transport error: %v", err)
			}
			assertErrResponse(t, joinResp.GetError(), 2003, "ROOM_NOT_FOUND")

			leaveResp, err := g.Leave(&roompb.LeaveRequest{UserId: "1"}, fakeRoomCtx(testRoomID))
			if err != nil {
				t.Fatalf("Leave transport error: %v", err)
			}
			assertErrResponse(t, leaveResp.GetError(), 2003, "ROOM_NOT_FOUND")

			postResp, err := g.PostMessage(graintest.NewPostMessageRequest("1", "hi"), fakeRoomCtx(testRoomID))
			if err != nil {
				t.Fatalf("PostMessage transport error: %v", err)
			}
			assertErrResponse(t, postResp.GetError(), 2003, "ROOM_NOT_FOUND")
		})
	}
}

func TestGrain_Activation_PanicsOnTransientLoadError(t *testing.T) {
	g := &room.Grain{}
	g.SetNotifier(&fakeNotifier{})
	g.SetLoader(stubRoomLoader{err: errFake("db unreachable")})
	g.UseSyncFanout()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Init: expected panic on a transient load error so the supervisor re-activates")
		}
	}()
	g.Init(fakeRoomCtx(testRoomID))
}

// Note: lifecycle logs (grain.activated / grain.passivated) are emitted by
// the receiver middleware, not the grain body. See
// internal/middleware/logging_test.go for those assertions.

func TestGrain_ReceiveDefault_LogsUnhandled(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	g, _, _ := newGrain(t)
	g.ReceiveDefault(graintest.NewFakeGrainContextWithMessage("general", struct{ X int }{X: 42}))

	if !strings.Contains(buf.String(), "grain.unhandled") {
		t.Errorf("ReceiveDefault did not emit grain.unhandled log: %s", buf.String())
	}
}

func TestGrain_NewKind_ReturnsRegisteredKind(t *testing.T) {
	k := room.NewKind(seededLoader())
	if k == nil {
		t.Fatal("NewKind: got nil, want non-nil *cluster.Kind")
	}
}

func TestGrain_NewKind_PanicsOnNilLoader(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewKind(nil): expected a wiring-time panic, got none")
		}
	}()
	room.NewKind(nil)
}

func TestGrain_FanOutNotifyError_LoggedNotFatal(t *testing.T) {
	g, notifier, _ := newGrain(t)
	mustJoin(t, g, "1")
	notifier.notifyCalls = nil
	notifier.notifyErrFn = func(string) error { return errFake("downstream user grain unreachable") }

	resp, err := g.Join(graintest.NewJoinRequest("2"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Errorf("expected success even when fan-out errors (best-effort), got error: %+v", resp.GetError())
	}
	if len(notifier.notifyCalls) != 2 {
		t.Errorf("notifyCalls: got %d, want 2 (loop must not abort on error)", len(notifier.notifyCalls))
	}
}

func TestGrain_DomainLogsCarryEnvelopeAttrs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	g, _, _ := newGrain(t)
	mustJoin(t, g, "1")

	out := buf.String()
	if !strings.Contains(out, `msg=room.member.joined`) {
		t.Errorf("logs missing room.member.joined line: %s", out)
	}
	if !strings.Contains(out, `grain_type=RoomGrain`) {
		t.Errorf("logs missing grain_type=RoomGrain: %s", out)
	}
	if !strings.Contains(out, `user_id=1`) {
		t.Errorf("logs missing user_id=1: %s", out)
	}
	if !strings.Contains(out, `msg=grain.fanout`) {
		t.Errorf("logs missing grain.fanout trace line: %s", out)
	}
	if !strings.Contains(out, `msg_type=Join.fanout`) {
		t.Errorf("logs missing msg_type=Join.fanout: %s", out)
	}
	if !strings.Contains(out, `target_count=1`) {
		t.Errorf("logs missing target_count=1: %s", out)
	}
}

// --- helpers -----------------------------------------------------------------

func mustJoin(t *testing.T, g *room.Grain, userID string) {
	t.Helper()
	resp, err := g.Join(graintest.NewJoinRequest(userID), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("Join(%q) unexpected error: %v", userID, err)
	}
	if resp.GetError() != nil {
		t.Fatalf("Join(%q) failed: %+v", userID, resp.GetError())
	}
}

func assertErrResponse(t *testing.T, ed *commonpb.ErrorDetail, wantCode int32, wantStatus string) {
	t.Helper()
	if ed == nil {
		t.Fatal("Error detail: got nil, want populated")
	}
	if ed.GetCode() != wantCode {
		t.Errorf("Error.Code: got %d, want %d", ed.GetCode(), wantCode)
	}
	if ed.GetStatus() != wantStatus {
		t.Errorf("Error.Status: got %q, want %q", ed.GetStatus(), wantStatus)
	}
	if ed.GetMessage() == "" {
		t.Errorf("Error.Message: must not be empty")
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

// Compile-time guard that the fakeNotifier satisfies the room package's
// internal userNotifier interface (exposed as room.UserNotifier in
// export_test.go).
var _ room.UserNotifier = (*fakeNotifier)(nil)
