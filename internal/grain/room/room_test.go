package room_test

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/id"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"
)

// mustUserID is a test helper that constructs a typed id.UserID, failing
// the test on any structural error. Used to keep table-driven cases
// readable without sprinkling NewUserID calls everywhere.
func mustUserID(t *testing.T, raw string) id.UserID {
	t.Helper()
	u, err := id.NewUserID(raw)
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
	forwardCalls []forwardCall
	notifyErrFn  func(userID string) error
	forwardErrFn func(userID string) error
}

type notifyCall struct {
	UserID    string
	RoomID    string
	Subject   string
	EventType userpb.RoomEventType
}

type forwardCall struct {
	UserID    string
	RoomID    string
	SenderID  string
	Text      string
	Timestamp time.Time
}

func (f *fakeNotifier) NotifyRoomEvent(userID id.UserID, req *userpb.NotifyRoomEventRequest) error {
	f.notifyCalls = append(f.notifyCalls, notifyCall{
		UserID:    userID.String(),
		RoomID:    req.GetRoomId(),
		Subject:   req.GetUserId(),
		EventType: req.GetEventType(),
	})
	if f.notifyErrFn != nil {
		return f.notifyErrFn(userID.String())
	}
	return nil
}

func (f *fakeNotifier) ForwardMessage(userID id.UserID, req *userpb.ForwardMessageRequest) error {
	f.forwardCalls = append(f.forwardCalls, forwardCall{
		UserID:    userID.String(),
		RoomID:    req.GetRoomId(),
		SenderID:  req.GetSenderId(),
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
	clock := time.UnixMilli(1000)
	g.SetClock(func() time.Time {
		clock = clock.Add(time.Millisecond)
		return clock
	})
	g.UseSyncFanout()
	g.Init(fakeRoomCtx("general"))
	return g, notifier, &clock
}

func TestGrain_Join(t *testing.T) {
	t.Run("success — empty room records member and fans out one JOINED event", func(t *testing.T) {
		g, notifier, _ := newGrain(t)

		resp, err := g.Join(&roompb.JoinRequest{UserId: "alice"}, fakeRoomCtx("general"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}

		if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "alice")}) {
			t.Errorf("Members: got %v, want [alice]", got)
		}
		if len(notifier.notifyCalls) != 1 {
			t.Fatalf("notifyCalls: got %d, want 1", len(notifier.notifyCalls))
		}
		c := notifier.notifyCalls[0]
		want := notifyCall{UserID: "alice", RoomID: "general", Subject: "alice", EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED}
		if c != want {
			t.Errorf("notifyCalls[0]: got %+v, want %+v", c, want)
		}
	})

	t.Run("success — fans out to N+1 members when joining a populated room", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "alice")
		mustJoin(t, g, "bob")
		notifier.notifyCalls = nil // reset before the third join

		resp, err := g.Join(&roompb.JoinRequest{UserId: "carol"}, fakeRoomCtx("general"))
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
		want := []string{"alice", "bob", "carol"}
		if !reflect.DeepEqual(gotRecipients, want) {
			t.Errorf("recipients: got %v, want %v", gotRecipients, want)
		}
		for i, c := range notifier.notifyCalls {
			if c.Subject != "carol" {
				t.Errorf("notifyCalls[%d].Subject: got %q, want carol", i, c.Subject)
			}
			if c.EventType != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED {
				t.Errorf("notifyCalls[%d].EventType: got %v, want JOINED", i, c.EventType)
			}
		}
	})

	t.Run("empty user_id returns 4001 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)

		resp, err := g.Join(&roompb.JoinRequest{UserId: ""}, fakeRoomCtx("general"))
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
		mustJoin(t, g, "alice")
		notifier.notifyCalls = nil

		resp, err := g.Join(&roompb.JoinRequest{UserId: "alice"}, fakeRoomCtx("general"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2002, "ROOM_ALREADY_MEMBER")
		if len(notifier.notifyCalls) != 0 {
			t.Errorf("notifyCalls: got %d, want 0", len(notifier.notifyCalls))
		}
		if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "alice")}) {
			t.Errorf("Members: got %v, want [alice]", got)
		}
	})
}

func TestGrain_Leave(t *testing.T) {
	t.Run("success — fans out LEFT to pre-removal snapshot including leaver", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "alice")
		mustJoin(t, g, "bob")
		notifier.notifyCalls = nil

		resp, err := g.Leave(&roompb.LeaveRequest{UserId: "alice"}, fakeRoomCtx("general"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}

		if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "bob")}) {
			t.Errorf("Members: got %v, want [bob]", got)
		}
		if len(notifier.notifyCalls) != 2 {
			t.Fatalf("notifyCalls: got %d, want 2 (alice, bob)", len(notifier.notifyCalls))
		}
		gotRecipients := []string{notifier.notifyCalls[0].UserID, notifier.notifyCalls[1].UserID}
		if !reflect.DeepEqual(gotRecipients, []string{"alice", "bob"}) {
			t.Errorf("recipients: got %v, want [alice bob]", gotRecipients)
		}
		for i, c := range notifier.notifyCalls {
			if c.Subject != "alice" || c.EventType != userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT {
				t.Errorf("notifyCalls[%d]: got %+v, want subject=alice eventType=LEFT", i, c)
			}
		}
	})

	t.Run("empty user_id returns 4001 with no fan-out", func(t *testing.T) {
		g, notifier, _ := newGrain(t)
		mustJoin(t, g, "alice")
		notifier.notifyCalls = nil

		resp, err := g.Leave(&roompb.LeaveRequest{UserId: ""}, fakeRoomCtx("general"))
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

		resp, err := g.Leave(&roompb.LeaveRequest{UserId: "alice"}, fakeRoomCtx("general"))
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
		mustJoin(t, g, "alice")
		mustJoin(t, g, "bob")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "hello"}, fakeRoomCtx("general"))
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
		if !reflect.DeepEqual(gotRecipients, []string{"alice", "bob"}) {
			t.Errorf("recipients: got %v, want [alice bob]", gotRecipients)
		}
		respTime := resp.GetTimestamp().AsTime()
		for i, c := range notifier.forwardCalls {
			if c.SenderID != "alice" || c.Text != "hello" || !c.Timestamp.Equal(respTime) {
				t.Errorf("forwardCalls[%d]: got %+v, want sender=alice text=hello ts=%v", i, c, respTime)
			}
		}
		if got := g.RecentMessageCount(); got != 1 {
			t.Errorf("RecentMessageCount: got %d, want 1", got)
		}
	})

	t.Run("two posts assign monotonically increasing timestamps and persist in buffer", func(t *testing.T) {
		g, _, _ := newGrain(t)
		mustJoin(t, g, "alice")

		resp1, _ := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "one"}, fakeRoomCtx("general"))
		resp2, _ := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "two"}, fakeRoomCtx("general"))

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
		mustJoin(t, g, "alice")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "", Text: "hi"}, fakeRoomCtx("general"))
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
		mustJoin(t, g, "alice")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: ""}, fakeRoomCtx("general"))
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
		mustJoin(t, g, "alice")
		notifier.forwardCalls = nil

		resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "  \t\n"}, fakeRoomCtx("general"))
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

		resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "hi"}, fakeRoomCtx("general"))
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
	mustJoin(t, g, "alice")
	mustJoin(t, g, "bob")
	notifier.forwardCalls = nil
	notifier.forwardErrFn = func(userID string) error {
		if userID == "bob" {
			return errFake("downstream user grain unreachable")
		}
		return nil
	}

	resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "hello"}, fakeRoomCtx("general"))
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
	g.UseSyncFanout()
	g.Init(fakeRoomCtx("general"))

	mustJoin(t, g, "alice")

	resp, err := g.PostMessage(&roompb.PostMessageRequest{UserId: "alice", Text: "hi"}, fakeRoomCtx("general"))
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
	k := room.NewKind()
	if k == nil {
		t.Fatal("NewKind: got nil, want non-nil *cluster.Kind")
	}
}

func TestGrain_FanOutNotifyError_LoggedNotFatal(t *testing.T) {
	g, notifier, _ := newGrain(t)
	mustJoin(t, g, "alice")
	notifier.notifyCalls = nil
	notifier.notifyErrFn = func(string) error { return errFake("downstream user grain unreachable") }

	resp, err := g.Join(&roompb.JoinRequest{UserId: "bob"}, fakeRoomCtx("general"))
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
	mustJoin(t, g, "alice")

	out := buf.String()
	if !strings.Contains(out, `msg=room.member.joined`) {
		t.Errorf("logs missing room.member.joined line: %s", out)
	}
	if !strings.Contains(out, `grain_type=RoomGrain`) {
		t.Errorf("logs missing grain_type=RoomGrain: %s", out)
	}
	if !strings.Contains(out, `user_id=alice`) {
		t.Errorf("logs missing user_id=alice: %s", out)
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
	resp, err := g.Join(&roompb.JoinRequest{UserId: userID}, fakeRoomCtx("general"))
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
