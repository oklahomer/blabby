package room_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"

	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
)

// fakeMembershipStore is a spy room.MembershipStore for unit tests: it records
// the actor of every RecordJoin/RecordLeave, returns a configurable event, and
// can be made to fail to exercise the grain's fail-closed path.
type fakeMembershipStore struct {
	loaded        []domain.UserRef
	loadErr       error
	event         room.MembershipEvent
	joinErr       error
	leaveErr      error
	roleErr       error
	transferErr   error
	joinCalls     []id.UserID
	leaveCalls    []id.UserID
	roleCalls     []roleChangeCall
	transferCalls []transferCall
}

// roleChangeCall records one RecordRoleChange invocation.
type roleChangeCall struct {
	actor  id.UserID
	target id.UserID
	role   domain.MembershipRole
}

// transferCall records one RecordOwnershipTransfer invocation.
type transferCall struct {
	actor    id.UserID
	newOwner id.UserID
}

func (f *fakeMembershipStore) LoadMembers(context.Context, id.RoomID) ([]domain.UserRef, error) {
	return f.loaded, f.loadErr
}

func (f *fakeMembershipStore) RecordJoin(_ context.Context, _ id.RoomID, actor domain.UserRef) (room.MembershipEvent, error) {
	f.joinCalls = append(f.joinCalls, actor.ID())
	if f.joinErr != nil {
		return room.MembershipEvent{}, f.joinErr
	}
	return f.event, nil
}

func (f *fakeMembershipStore) RecordLeave(_ context.Context, _ id.RoomID, actor domain.UserRef) (room.MembershipEvent, error) {
	f.leaveCalls = append(f.leaveCalls, actor.ID())
	if f.leaveErr != nil {
		return room.MembershipEvent{}, f.leaveErr
	}
	return f.event, nil
}

func (f *fakeMembershipStore) RecordRoleChange(_ context.Context, _ id.RoomID, actor, target id.UserID, role domain.MembershipRole) error {
	f.roleCalls = append(f.roleCalls, roleChangeCall{actor: actor, target: target, role: role})
	return f.roleErr
}

func (f *fakeMembershipStore) RecordOwnershipTransfer(_ context.Context, _ id.RoomID, actor, newOwner id.UserID) error {
	f.transferCalls = append(f.transferCalls, transferCall{actor: actor, newOwner: newOwner})
	return f.transferErr
}

// userRefFor builds a validated UserRef for tests seeding the member cache.
func userRefFor(t *testing.T, rawID, name string) domain.UserRef {
	t.Helper()
	code, err := id.NewPublicCode()
	if err != nil {
		t.Fatalf("NewPublicCode: %v", err)
	}
	ref, err := domain.NewUserRef(mustUserID(t, rawID), code, name)
	if err != nil {
		t.Fatalf("user ref %s/%s: %v", rawID, name, err)
	}
	return ref
}

// eventFixture is a non-zero MembershipEvent the spy returns so tests can assert
// the id and timestamp travel onto the fan-out.
func eventFixture(t *testing.T) room.MembershipEvent {
	t.Helper()
	eid, err := id.NewEventID(987654321)
	if err != nil {
		t.Fatalf("event id: %v", err)
	}
	return room.MembershipEvent{ID: eid, OccurredAt: time.UnixMilli(1_700_000_000_000)}
}

// newStoreGrain builds a loaded grain wired with store and a recording notifier.
func newStoreGrain(t *testing.T, store room.MembershipStore) (*room.Grain, *fakeNotifier) {
	t.Helper()
	g := &room.Grain{}
	notifier := &fakeNotifier{}
	g.SetNotifier(notifier)
	g.SetLoader(seededLoader(roomRef(t, testRoomID, domain.RoomStatusActive)))
	g.SetMembershipStore(store)
	g.UseSyncFanout()
	g.Init(fakeRoomCtx(testRoomID))
	return g, notifier
}

func TestGrain_Join_PersistsAndCarriesEvent(t *testing.T) {
	evt := eventFixture(t)
	store := &fakeMembershipStore{event: evt}
	g, notifier := newStoreGrain(t, store)

	resp, err := g.Join(graintest.NewJoinRequestNamed("1", "Alice"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("expected success, got error: %+v", resp.GetError())
	}

	if want := []id.UserID{mustUserID(t, "1")}; !reflect.DeepEqual(store.joinCalls, want) {
		t.Errorf("RecordJoin actors: got %v, want %v", store.joinCalls, want)
	}
	if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "1")}) {
		t.Errorf("Members: got %v, want [1]", got)
	}
	if len(notifier.notifyReqs) != 1 {
		t.Fatalf("notifyReqs: got %d, want 1", len(notifier.notifyReqs))
	}
	req := notifier.notifyReqs[0]
	if req.GetEventId() != "987654321" {
		t.Errorf("fan-out EventId: got %q, want %q", req.GetEventId(), "987654321")
	}
	if !req.GetTimestamp().AsTime().Equal(evt.OccurredAt) {
		t.Errorf("fan-out Timestamp: got %v, want %v", req.GetTimestamp().AsTime(), evt.OccurredAt)
	}
	if req.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED {
		t.Errorf("fan-out EventType: got %v, want JOINED", req.GetEventType())
	}
}

func TestGrain_Join_FailClosedOnWriteError(t *testing.T) {
	store := &fakeMembershipStore{joinErr: errFake("db write failed")}
	g, notifier := newStoreGrain(t, store)

	resp, err := g.Join(graintest.NewJoinRequest("1"), fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")

	// Fail-closed: no in-memory member added, no fan-out, but the room ref is
	// still returned (the grain was loaded).
	if got := g.Members(); len(got) != 0 {
		t.Errorf("Members after failed write: got %v, want empty", got)
	}
	if len(notifier.notifyCalls) != 0 {
		t.Errorf("notifyCalls after failed write: got %d, want 0", len(notifier.notifyCalls))
	}
	if resp.GetRoom() == nil {
		t.Error("fail-closed Join response should still carry the room ref")
	}
}

func TestGrain_Leave_PersistsAndCarriesEvent(t *testing.T) {
	evt := eventFixture(t)
	store := &fakeMembershipStore{
		loaded: []domain.UserRef{userRefFor(t, "1", "Alice")},
		event:  evt,
	}
	g, notifier := newStoreGrain(t, store)

	resp, err := g.Leave(&roompb.LeaveRequest{UserId: "1"}, fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetError() != nil {
		t.Fatalf("expected success, got error: %+v", resp.GetError())
	}

	if want := []id.UserID{mustUserID(t, "1")}; !reflect.DeepEqual(store.leaveCalls, want) {
		t.Errorf("RecordLeave actors: got %v, want %v", store.leaveCalls, want)
	}
	if got := g.Members(); len(got) != 0 {
		t.Errorf("Members after leave: got %v, want empty", got)
	}
	if len(notifier.notifyReqs) != 1 {
		t.Fatalf("notifyReqs: got %d, want 1 (LEFT to the leaver)", len(notifier.notifyReqs))
	}
	req := notifier.notifyReqs[0]
	if req.GetEventId() != "987654321" {
		t.Errorf("fan-out EventId: got %q, want %q", req.GetEventId(), "987654321")
	}
	if req.GetEventType() != userpb.RoomEventType_ROOM_EVENT_TYPE_LEFT {
		t.Errorf("fan-out EventType: got %v, want LEFT", req.GetEventType())
	}
}

func TestGrain_Leave_FailClosedOnWriteError(t *testing.T) {
	store := &fakeMembershipStore{
		loaded:   []domain.UserRef{userRefFor(t, "1", "Alice")},
		leaveErr: errFake("db delete failed"),
	}
	g, notifier := newStoreGrain(t, store)

	resp, err := g.Leave(&roompb.LeaveRequest{UserId: "1"}, fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")

	// Fail-closed: the member is still present and nothing fanned out.
	if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "1")}) {
		t.Errorf("Members after failed delete: got %v, want [1] (unchanged)", got)
	}
	if len(notifier.notifyCalls) != 0 {
		t.Errorf("notifyCalls after failed delete: got %d, want 0", len(notifier.notifyCalls))
	}
}

func TestGrain_Leave_OwnerIsRefused(t *testing.T) {
	store := &fakeMembershipStore{
		loaded:   []domain.UserRef{userRefFor(t, "1", "Alice")},
		leaveErr: persistence.ErrOwnerCannotLeave,
	}
	g, notifier := newStoreGrain(t, store)

	resp, err := g.Leave(&roompb.LeaveRequest{UserId: "1"}, fakeRoomCtx(testRoomID))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertErrResponse(t, resp.GetError(), 2004, "ROOM_OWNER_CANNOT_LEAVE")

	// The refusal is a business outcome, not a write failure: the member cache
	// keeps the owner and nothing fans out.
	if got := g.Members(); !reflect.DeepEqual(got, []id.UserID{mustUserID(t, "1")}) {
		t.Errorf("Members after refused leave: got %v, want [1] (unchanged)", got)
	}
	if len(notifier.notifyCalls) != 0 {
		t.Errorf("notifyCalls after refused leave: got %d, want 0", len(notifier.notifyCalls))
	}
}

func TestGrain_Activation_LoadsMembers(t *testing.T) {
	store := &fakeMembershipStore{
		loaded: []domain.UserRef{userRefFor(t, "1", "Alice"), userRefFor(t, "2", "Bob")},
	}
	g, _ := newStoreGrain(t, store)

	want := []id.UserID{mustUserID(t, "1"), mustUserID(t, "2")}
	if got := g.Members(); !reflect.DeepEqual(got, want) {
		t.Errorf("Members after activation: got %v, want %v", got, want)
	}
}

func TestGrain_Activation_PanicsOnMemberLoadError(t *testing.T) {
	g := &room.Grain{}
	g.SetNotifier(&fakeNotifier{})
	g.SetLoader(seededLoader(roomRef(t, testRoomID, domain.RoomStatusActive)))
	g.SetMembershipStore(&fakeMembershipStore{loadErr: errFake("db unreachable")})
	g.UseSyncFanout()

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Init: expected panic on a transient member-load error so the supervisor re-activates")
		}
	}()
	g.Init(fakeRoomCtx(testRoomID))
}
