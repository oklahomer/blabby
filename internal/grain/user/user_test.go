package user_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"
	graintest "github.com/oklahomer/blabby/internal/testutil/grain"
)

// mustRoomID is a test helper that constructs a typed id.RoomID, failing
// the test on any structural error.
func mustRoomID(t *testing.T, raw string) id.RoomID {
	t.Helper()
	r, err := id.ParseRoomID(raw)
	if err != nil {
		t.Fatalf("mustRoomID(%q): %v", raw, err)
	}
	return r
}

// stubRoomCode is a valid bare 10-symbol Crockford public_code for Join stubs
// that do not assert on the rendered R… code.
const stubRoomCode = "G000000004"

// stubRoomRef builds the RoomRef a loaded Room grain returns on Join, so the
// User grain can parse and cache it. The RoomId mirrors the addressed room; the
// public code is a fixed valid stand-in.
func stubRoomRef(roomID string) *commonpb.RoomRef {
	return &commonpb.RoomRef{
		RoomId:     roomID,
		PublicCode: stubRoomCode,
		Name:       "Room " + roomID,
		Status:     "active",
	}
}

// fakeUserCtx returns a fake grain context with kind="UserGrain", matching
// what cluster.NewKind("UserGrain", ...) produces in production. Handlers
// in this package now derive grain_type from ctx.Kind(), so tests have to
// populate it.
func fakeUserCtx(identity string, opts ...graintest.FakeGrainContextOption) cluster.GrainContext {
	return graintest.NewFakeGrainContext(identity, append([]graintest.FakeGrainContextOption{graintest.WithKind("UserGrain")}, opts...)...)
}

// fakeRoomClient is a recording roomClient. Each method records its inputs
// for assertion and returns the response/error configured by the test.
type fakeRoomClient struct {
	mu sync.Mutex

	joinCalls       []joinCall
	leaveCalls      []leaveCall
	postCalls       []postCall
	setRoleCalls    []setRoleCall
	transferCalls   []transferOwnershipCall
	joinFn          func(roomID id.RoomID, req *roompb.JoinRequest) (*roompb.JoinResponse, error)
	leaveFn         func(roomID id.RoomID, req *roompb.LeaveRequest) (*roompb.LeaveResponse, error)
	postFn          func(roomID id.RoomID, req *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error)
	setRoleFn       func(roomID id.RoomID, req *roompb.SetMemberRoleRequest) (*roompb.SetMemberRoleResponse, error)
	transferFn      func(roomID id.RoomID, req *roompb.TransferOwnershipRequest) (*roompb.TransferOwnershipResponse, error)
	defaultJoin     *roompb.JoinResponse
	defaultLeave    *roompb.LeaveResponse
	defaultPost     *roompb.PostMessageResponse
	defaultSetRole  *roompb.SetMemberRoleResponse
	defaultTransfer *roompb.TransferOwnershipResponse
}

// userRef groups a user's id and display name the way the production proto
// (commonpb.UserRef) carries them, so the recorders assert the pair travels
// together to the Room grain rather than as two loose strings.
type userRef struct {
	ID   string
	Name string
}

type joinCall struct {
	RoomID string
	User   userRef
}
type leaveCall struct {
	RoomID string
	UserID string
}
type postCall struct {
	RoomID string
	User   userRef
	Text   string
}
type setRoleCall struct {
	RoomID string
	Actor  userRef
	Target string
	Role   string
}
type transferOwnershipCall struct {
	RoomID   string
	Actor    userRef
	NewOwner string
}

func (f *fakeRoomClient) Join(roomID id.RoomID, req *roompb.JoinRequest) (*roompb.JoinResponse, error) {
	f.mu.Lock()
	f.joinCalls = append(f.joinCalls, joinCall{RoomID: roomID.String(), User: userRef{ID: req.GetUser().GetId(), Name: req.GetUser().GetName()}})
	fn := f.joinFn
	def := f.defaultJoin
	f.mu.Unlock()
	if fn != nil {
		return fn(roomID, req)
	}
	if def != nil {
		return def, nil
	}
	return &roompb.JoinResponse{Room: stubRoomRef(roomID.String())}, nil
}

func (f *fakeRoomClient) Leave(roomID id.RoomID, req *roompb.LeaveRequest) (*roompb.LeaveResponse, error) {
	f.mu.Lock()
	f.leaveCalls = append(f.leaveCalls, leaveCall{RoomID: roomID.String(), UserID: req.GetUserId()})
	fn := f.leaveFn
	def := f.defaultLeave
	f.mu.Unlock()
	if fn != nil {
		return fn(roomID, req)
	}
	if def != nil {
		return def, nil
	}
	return &roompb.LeaveResponse{}, nil
}

func (f *fakeRoomClient) PostMessage(roomID id.RoomID, req *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error) {
	f.mu.Lock()
	f.postCalls = append(f.postCalls, postCall{RoomID: roomID.String(), User: userRef{ID: req.GetUser().GetId(), Name: req.GetUser().GetName()}, Text: req.GetText()})
	fn := f.postFn
	def := f.defaultPost
	f.mu.Unlock()
	if fn != nil {
		return fn(roomID, req)
	}
	if def != nil {
		return def, nil
	}
	return &roompb.PostMessageResponse{Timestamp: timestamppb.New(time.UnixMilli(12345))}, nil
}

func (f *fakeRoomClient) SetMemberRole(roomID id.RoomID, req *roompb.SetMemberRoleRequest) (*roompb.SetMemberRoleResponse, error) {
	f.mu.Lock()
	f.setRoleCalls = append(f.setRoleCalls, setRoleCall{
		RoomID: roomID.String(),
		Actor:  userRef{ID: req.GetActor().GetId(), Name: req.GetActor().GetName()},
		Target: req.GetTargetUserId(),
		Role:   req.GetRole(),
	})
	fn := f.setRoleFn
	def := f.defaultSetRole
	f.mu.Unlock()
	if fn != nil {
		return fn(roomID, req)
	}
	if def != nil {
		return def, nil
	}
	return &roompb.SetMemberRoleResponse{}, nil
}

func (f *fakeRoomClient) TransferOwnership(roomID id.RoomID, req *roompb.TransferOwnershipRequest) (*roompb.TransferOwnershipResponse, error) {
	f.mu.Lock()
	f.transferCalls = append(f.transferCalls, transferOwnershipCall{
		RoomID:   roomID.String(),
		Actor:    userRef{ID: req.GetActor().GetId(), Name: req.GetActor().GetName()},
		NewOwner: req.GetNewOwnerUserId(),
	})
	fn := f.transferFn
	def := f.defaultTransfer
	f.mu.Unlock()
	if fn != nil {
		return fn(roomID, req)
	}
	if def != nil {
		return def, nil
	}
	return &roompb.TransferOwnershipResponse{}, nil
}

// recordingSender captures every fan-out call so tests can assert delivery
// fan-out (count, recipients, payload identity).
type recordingSender struct {
	mu    sync.Mutex
	calls []sendCall
}

type sendCall struct {
	PID *actor.PID
	Msg proto.Message
}

func (r *recordingSender) Send() user.PIDSender {
	return func(pid *actor.PID, msg proto.Message) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, sendCall{PID: pid, Msg: msg})
	}
}

func (r *recordingSender) Calls() []sendCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sendCall, len(r.calls))
	copy(out, r.calls)
	return out
}

// grainHarness bundles a Grain with the recorders the tests assert against.
type grainHarness struct {
	g       *user.Grain
	rooms   *fakeRoomClient
	sender  *recordingSender
	watcher *graintest.WatchRecorder
}

// newGrain returns an initialized Grain wired with a fakeRoomClient, a
// recordingSender, and a Watch recorder, identified as user "1". The
// Watch recorder is shared with every helper that calls RegisterConnection
// through ctxWithWatch().
func newGrain(t *testing.T) *grainHarness {
	t.Helper()
	g := &user.Grain{}
	rc := &fakeRoomClient{}
	g.SetRoomClient(rc)
	sender := &recordingSender{}
	g.SetSender(sender.Send())
	g.Init(fakeUserCtx("1"))
	return &grainHarness{g: g, rooms: rc, sender: sender, watcher: &graintest.WatchRecorder{}}
}

// --- RegisterConnection -------------------------------------------------

func TestGrain_RegisterConnection(t *testing.T) {
	t.Run("success records connection PID and arms Watch", func(t *testing.T) {
		h := newGrain(t)
		pid := actor.NewPID("addr", "conn-1")

		resp, err := h.g.RegisterConnection(
			pidRegisterReq(pid),
			fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		if got := h.g.Connections(); !reflect.DeepEqual(got, []*actor.PID{pid}) {
			t.Errorf("Connections: got %v, want [%v]", got, pid)
		}
		if got := h.watcher.PIDs(); !reflect.DeepEqual(got, []*actor.PID{pid}) {
			t.Errorf("Watch PIDs: got %v, want [%v]", got, pid)
		}
	})

	t.Run("missing requester_pid returns 4001", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.RegisterConnection(
			&userpb.RegisterConnectionRequest{},
			fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if got := h.watcher.PIDs(); len(got) != 0 {
			t.Errorf("Watch PIDs on validation failure: got %v, want []", got)
		}
	})

	t.Run("missing requester_pid.address returns 4001", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.RegisterConnection(
			&userpb.RegisterConnectionRequest{
				RequesterPid: &userpb.PID{Id: "id"},
			},
			fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
	})

	t.Run("missing requester_pid.id returns 4001", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.RegisterConnection(
			&userpb.RegisterConnectionRequest{
				RequesterPid: &userpb.PID{Address: "addr"},
			},
			fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
	})

	t.Run("re-register same PID is idempotent", func(t *testing.T) {
		h := newGrain(t)
		pid := actor.NewPID("addr", "conn-1")

		_, _ = h.g.RegisterConnection(pidRegisterReq(pid), fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)))
		resp, err := h.g.RegisterConnection(pidRegisterReq(pid), fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)))
		if err != nil {
			t.Fatalf("unexpected error on re-register: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected re-register to succeed, got error: %+v", resp.GetError())
		}
		if got := h.g.Connections(); !reflect.DeepEqual(got, []*actor.PID{pid}) {
			t.Errorf("Connections: got %v, want [%v] (size unchanged)", got, pid)
		}
		// Documenting current behavior: the grain calls ctx.Watch on
		// every Register, relying on protoactor's watcher-set dedupe.
		// If a future change short-circuits Register-when-already-present
		// (or protoactor stops deduping), this assertion will catch it.
		if got := len(h.watcher.PIDs()); got != 2 {
			t.Errorf("Watch calls: got %d, want 2 (one per Register; protoactor dedupes)", got)
		}
	})
}

// --- Terminated eviction (Watch-driven cleanup, ADR-012) ----------------

func TestGrain_Terminated_EvictsConnection(t *testing.T) {
	t.Run("Terminated for known PID drops the entry", func(t *testing.T) {
		h := newGrain(t)
		pid := actor.NewPID("addr", "conn-1")
		mustRegister(t, h, pid)

		h.g.ReceiveDefault(fakeUserCtx("1",
			graintest.WithMessage(&actor.Terminated{Who: pid}),
		))

		if got := h.g.Connections(); len(got) != 0 {
			t.Errorf("Connections: got %v, want []", got)
		}
	})

	t.Run("Terminated for unknown PID is a no-op", func(t *testing.T) {
		h := newGrain(t)
		pidLive := actor.NewPID("addr", "conn-live")
		mustRegister(t, h, pidLive)
		pidStranger := actor.NewPID("addr", "stranger")

		h.g.ReceiveDefault(fakeUserCtx("1",
			graintest.WithMessage(&actor.Terminated{Who: pidStranger}),
		))

		if got := h.g.Connections(); !reflect.DeepEqual(got, []*actor.PID{pidLive}) {
			t.Errorf("Connections: got %v, want [%v]", got, pidLive)
		}
	})

	t.Run("Terminated evicts only the matching PID when multiple are registered", func(t *testing.T) {
		h := newGrain(t)
		pidA := actor.NewPID("addr", "conn-A")
		pidB := actor.NewPID("addr", "conn-B")
		mustRegister(t, h, pidA)
		mustRegister(t, h, pidB)

		h.g.ReceiveDefault(fakeUserCtx("1",
			graintest.WithMessage(&actor.Terminated{Who: pidA}),
		))

		if got := h.g.Connections(); !reflect.DeepEqual(got, []*actor.PID{pidB}) {
			t.Errorf("Connections: got %v, want [%v]", got, pidB)
		}
	})
}

// --- JoinRoom ------------------------------------------------------------

func TestGrain_JoinRoom(t *testing.T) {
	t.Run("success records room and forwards user_id", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		if got := h.g.JoinedRooms(); !reflect.DeepEqual(got, []id.RoomID{mustRoomID(t, "4")}) {
			t.Errorf("JoinedRooms: got %v, want [general]", got)
		}
		if len(h.rooms.joinCalls) != 1 || h.rooms.joinCalls[0] != (joinCall{RoomID: "4", User: userRef{ID: "1", Name: "1"}}) {
			t.Errorf("joinCalls: got %+v, want [{general {alice alice}}]", h.rooms.joinCalls)
		}
	})

	t.Run("empty room_id returns 4001 with no Room call", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: ""}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(h.rooms.joinCalls) != 0 {
			t.Errorf("joinCalls: got %d, want 0", len(h.rooms.joinCalls))
		}
	})

	t.Run("already-member response reconciles joined_rooms and succeeds", func(t *testing.T) {
		h := newGrain(t)
		// A loaded grain carries its RoomRef even on the already-member outcome,
		// so the User grain still caches it on the repair path.
		h.rooms.defaultJoin = &roompb.JoinResponse{
			Error: &commonpb.ErrorDetail{Code: 2002, Status: "ROOM_ALREADY_MEMBER", Message: "already a member"},
			Room:  stubRoomRef("4"),
		}

		resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		if got := h.g.JoinedRooms(); !reflect.DeepEqual(got, []id.RoomID{mustRoomID(t, "4")}) {
			t.Errorf("JoinedRooms: got %v, want [general]", got)
		}
	})

	t.Run("other Room grain business errors are copied through", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.defaultJoin = &roompb.JoinResponse{
			Error: &commonpb.ErrorDetail{Code: 2003, Status: "ROOM_NOT_FOUND", Message: "room not found"},
		}

		resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2003, "ROOM_NOT_FOUND")
		if got := h.g.JoinedRooms(); len(got) != 0 {
			t.Errorf("JoinedRooms: got %v, want []", got)
		}
	})

	t.Run("malformed Room grain error fails closed", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.defaultJoin = &roompb.JoinResponse{
			Error: &commonpb.ErrorDetail{Code: 2003, Status: "ROOM_NOT_MEMBER", Message: "bad pair"},
		}

		resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
		if got := h.g.JoinedRooms(); len(got) != 0 {
			t.Errorf("JoinedRooms: got %v, want []", got)
		}
	})

	t.Run("transport error becomes 5001", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.joinFn = func(id.RoomID, *roompb.JoinRequest) (*roompb.JoinResponse, error) {
			return nil, errors.New("dial timeout")
		}

		resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
		if got := h.g.JoinedRooms(); len(got) != 0 {
			t.Errorf("JoinedRooms: got %v, want []", got)
		}
	})
}

// --- LeaveRoom -----------------------------------------------------------

func TestGrain_LeaveRoom(t *testing.T) {
	t.Run("success removes room and forwards user_id", func(t *testing.T) {
		h := newGrain(t)
		_, _ = h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		h.rooms.joinCalls = nil

		resp, err := h.g.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		if got := h.g.JoinedRooms(); len(got) != 0 {
			t.Errorf("JoinedRooms: got %v, want []", got)
		}
		if len(h.rooms.leaveCalls) != 1 || h.rooms.leaveCalls[0] != (leaveCall{RoomID: "4", UserID: "1"}) {
			t.Errorf("leaveCalls: got %+v, want [{general alice}]", h.rooms.leaveCalls)
		}
	})

	t.Run("empty room_id returns 4001", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: ""}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
		if len(h.rooms.leaveCalls) != 0 {
			t.Errorf("leaveCalls: got %d, want 0", len(h.rooms.leaveCalls))
		}
	})

	t.Run("not-member response reconciles joined_rooms and succeeds", func(t *testing.T) {
		h := newGrain(t)
		_, _ = h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		h.rooms.defaultLeave = &roompb.LeaveResponse{
			Error: &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member"},
		}

		resp, err := h.g.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil {
			t.Fatalf("expected success, got error: %+v", resp.GetError())
		}
		if got := h.g.JoinedRooms(); len(got) != 0 {
			t.Errorf("JoinedRooms: got %v, want []", got)
		}
	})

	t.Run("other Room grain business errors are copied through", func(t *testing.T) {
		h := newGrain(t)
		_, _ = h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		h.rooms.defaultLeave = &roompb.LeaveResponse{
			Error: &commonpb.ErrorDetail{Code: 2003, Status: "ROOM_NOT_FOUND", Message: "room not found"},
		}

		resp, err := h.g.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2003, "ROOM_NOT_FOUND")
		if got := h.g.JoinedRooms(); !reflect.DeepEqual(got, []id.RoomID{mustRoomID(t, "4")}) {
			t.Errorf("JoinedRooms: got %v, want [general]", got)
		}
	})

	t.Run("malformed Room grain error fails closed", func(t *testing.T) {
		h := newGrain(t)
		_, _ = h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		h.rooms.defaultLeave = &roompb.LeaveResponse{
			Error: &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_FOUND", Message: "bad pair"},
		}

		resp, err := h.g.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
		if got := h.g.JoinedRooms(); !reflect.DeepEqual(got, []id.RoomID{mustRoomID(t, "4")}) {
			t.Errorf("JoinedRooms: got %v, want [general]", got)
		}
	})

	t.Run("transport error becomes 5001", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.leaveFn = func(id.RoomID, *roompb.LeaveRequest) (*roompb.LeaveResponse, error) {
			return nil, errors.New("dial timeout")
		}

		resp, err := h.g.LeaveRoom(&userpb.LeaveRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
	})
}

// --- SendMessage ---------------------------------------------------------

func TestGrain_SendMessage(t *testing.T) {
	t.Run("success returns Room grain timestamp without local fan-out", func(t *testing.T) {
		h := newGrain(t)
		mustRegister(t, h, actor.NewPID("addr", "conn-1"))
		want := time.UnixMilli(9999)
		h.rooms.defaultPost = &roompb.PostMessageResponse{Timestamp: timestamppb.New(want)}

		resp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: "hi"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetError() != nil || !resp.GetTimestamp().AsTime().Equal(want) {
			t.Fatalf("got %+v, want error=nil ts=%v", resp, want)
		}
		if len(h.rooms.postCalls) != 1 || h.rooms.postCalls[0] != (postCall{RoomID: "4", User: userRef{ID: "1", Name: "1"}, Text: "hi"}) {
			t.Errorf("postCalls: got %+v, want one call with user={alice alice} text=hi", h.rooms.postCalls)
		}
		if got := h.sender.Calls(); len(got) != 0 {
			t.Errorf("sender.Calls: got %d, want 0 (SendMessage must not echo locally)", len(got))
		}
	})

	t.Run("whitespace-only text returns 4002", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: " \t\n"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4002, "MISSING_FIELD")
		if len(h.rooms.postCalls) != 0 {
			t.Errorf("postCalls: got %d, want 0", len(h.rooms.postCalls))
		}
	})

	t.Run("empty room_id returns 4001", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "", Text: "hi"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 4001, "INVALID_REQUEST")
	})

	t.Run("Room grain returns 2001 propagates inline with timestamp 0", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.defaultPost = &roompb.PostMessageResponse{
			Error: &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member"},
		}

		resp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: "hi"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 2001, "ROOM_NOT_MEMBER")
		if resp.GetTimestamp() != nil {
			t.Errorf("Timestamp: got %v, want nil on failure", resp.GetTimestamp())
		}
	})

	t.Run("malformed Room grain error fails closed", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.defaultPost = &roompb.PostMessageResponse{
			Error: &commonpb.ErrorDetail{Code: 2001, Status: "ROOM_NOT_FOUND", Message: "bad pair"},
		}

		resp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: "hi"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
		if resp.GetTimestamp() != nil {
			t.Errorf("Timestamp: got %v, want nil on failure", resp.GetTimestamp())
		}
	})

	t.Run("transport error becomes 5001", func(t *testing.T) {
		h := newGrain(t)
		h.rooms.postFn = func(id.RoomID, *roompb.PostMessageRequest) (*roompb.PostMessageResponse, error) {
			return nil, errors.New("dial timeout")
		}

		resp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: "hi"}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
	})
}

// --- ForwardMessage ------------------------------------------------------

func TestGrain_ForwardMessage(t *testing.T) {
	t.Run("with N connections fans out N times carrying the same proto", func(t *testing.T) {
		h := newGrain(t)
		mustRegister(t, h, actor.NewPID("addr", "conn-a"))
		mustRegister(t, h, actor.NewPID("addr", "conn-b"))
		mustRegister(t, h, actor.NewPID("addr", "conn-c"))

		req := &userpb.ForwardMessageRequest{Room: &commonpb.RoomRef{RoomId: "4"}, Sender: &commonpb.UserRef{Id: "1", Name: "Alice Example"}, Text: "hello", Timestamp: timestamppb.New(time.UnixMilli(42))}
		resp, err := h.g.ForwardMessage(req, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp == nil {
			t.Errorf("expected non-nil response")
		}

		calls := h.sender.Calls()
		if len(calls) != 3 {
			t.Fatalf("sender.Calls: got %d, want 3", len(calls))
		}
		for i, c := range calls {
			if c.Msg != proto.Message(req) {
				t.Errorf("calls[%d].Msg: got %p, want %p (must reuse inbound proto)", i, c.Msg, req)
			}
		}
	})

	t.Run("with 0 connections returns success and does not call sender", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.ForwardMessage(&userpb.ForwardMessageRequest{Room: &commonpb.RoomRef{RoomId: "4"}, Sender: &commonpb.UserRef{Id: "1", Name: "Alice Example"}}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp == nil {
			t.Errorf("expected non-nil response (Room grain delivers regardless of connection state)")
		}
		if got := h.sender.Calls(); len(got) != 0 {
			t.Errorf("sender.Calls: got %d, want 0", len(got))
		}
	})
}

// --- NotifyRoomEvent -----------------------------------------------------

func TestGrain_NotifyRoomEvent(t *testing.T) {
	t.Run("with N connections fans out N times and does NOT mutate joined_rooms", func(t *testing.T) {
		h := newGrain(t)
		mustRegister(t, h, actor.NewPID("addr", "conn-a"))
		mustRegister(t, h, actor.NewPID("addr", "conn-b"))
		before := h.g.JoinedRooms()

		req := &userpb.NotifyRoomEventRequest{
			Room:      &commonpb.RoomRef{RoomId: "4"},
			User:      &commonpb.UserRef{Id: "2", Name: "Bob Example"},
			EventType: userpb.RoomEventType_ROOM_EVENT_TYPE_JOINED,
		}
		resp, err := h.g.NotifyRoomEvent(req, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp == nil {
			t.Errorf("expected non-nil response")
		}
		if calls := h.sender.Calls(); len(calls) != 2 {
			t.Fatalf("sender.Calls: got %d, want 2", len(calls))
		}

		after := h.g.JoinedRooms()
		if !reflect.DeepEqual(before, after) {
			t.Errorf("joined_rooms changed: before=%v after=%v (NotifyRoomEvent must not mutate)", before, after)
		}
	})
}

// --- GetJoinedRooms ------------------------------------------------------

func TestGrain_GetJoinedRooms(t *testing.T) {
	t.Run("empty returns empty list", func(t *testing.T) {
		h := newGrain(t)

		resp, err := h.g.GetJoinedRooms(&userpb.GetJoinedRoomsRequest{}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetRooms()) != 0 {
			t.Errorf("Rooms: got %v, want []", resp.GetRooms())
		}
	})

	t.Run("after two joins returns cached refs sorted by room id", func(t *testing.T) {
		h := newGrain(t)
		_, _ = h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "22"}, fakeUserCtx("1"))
		_, _ = h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "20"}, fakeUserCtx("1"))

		resp, err := h.g.GetJoinedRooms(&userpb.GetJoinedRoomsRequest{}, fakeUserCtx("1"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		rooms := resp.GetRooms()
		gotIDs := make([]string, len(rooms))
		for i, room := range rooms {
			gotIDs[i] = room.GetRoomId()
		}
		if want := []string{"20", "22"}; !reflect.DeepEqual(gotIDs, want) {
			t.Errorf("room ids: got %v, want %v", gotIDs, want)
		}
		// The refs carry renderable metadata, not just ids.
		for _, room := range rooms {
			if room.GetPublicCode() == "" || room.GetName() == "" {
				t.Errorf("room %q: got empty public_code/name, want populated ref", room.GetRoomId())
			}
		}
	})
}

func TestGrain_JoinRoom_MissingRoomRefFailsClosed(t *testing.T) {
	h := newGrain(t)
	// A loaded grain must return its RoomRef on success; a Join response without
	// one is a backend contract breach. The User grain fails closed and records
	// no membership rather than caching a degraded ref.
	h.rooms.defaultJoin = &roompb.JoinResponse{}

	resp, err := h.g.JoinRoom(&userpb.JoinRoomRequest{RoomId: "4"}, fakeUserCtx("1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertErrResponse(t, resp.GetError(), 5001, "INTERNAL_ERROR")
	if got := h.g.JoinedRooms(); len(got) != 0 {
		t.Errorf("JoinedRooms: got %v, want [] (no membership recorded on a malformed ref)", got)
	}
}

// --- Multi-device echo (cross-method) ------------------------------------

func TestGrain_MultiDeviceEcho(t *testing.T) {
	h := newGrain(t)
	pidA := actor.NewPID("addr", "device-A")
	pidB := actor.NewPID("addr", "device-B")
	mustRegister(t, h, pidA)
	mustRegister(t, h, pidB)
	h.rooms.defaultPost = &roompb.PostMessageResponse{Timestamp: timestamppb.New(time.UnixMilli(7))}

	// 1. SendMessage: alice posts "hi" — Room grain returns success.
	sendResp, err := h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: "hi"}, fakeUserCtx("1"))
	if err != nil {
		t.Fatalf("SendMessage unexpected error: %v", err)
	}
	if sendResp.GetError() != nil {
		t.Fatalf("SendMessage error: %+v", sendResp.GetError())
	}
	// 2. Critical: SendMessage MUST NOT echo locally — echo is the Room
	// grain's job, delivered back via ForwardMessage.
	if got := h.sender.Calls(); len(got) != 0 {
		t.Fatalf("sender.Calls after SendMessage: got %d, want 0 (echo must come from Room grain)", len(got))
	}

	// 3. Simulate Room grain fan-out back to alice.
	fwd := &userpb.ForwardMessageRequest{Room: &commonpb.RoomRef{RoomId: "4"}, Sender: &commonpb.UserRef{Id: "1", Name: "Alice Example"}, Text: "hi", Timestamp: timestamppb.New(time.UnixMilli(7))}
	_, err = h.g.ForwardMessage(fwd, fakeUserCtx("1"))
	if err != nil {
		t.Fatalf("ForwardMessage unexpected error: %v", err)
	}

	// 4. Both devices receive the message. PIDs are sorted by their
	// canonical string form — "addr/device-A" < "addr/device-B".
	calls := h.sender.Calls()
	if len(calls) != 2 {
		t.Fatalf("sender.Calls after ForwardMessage: got %d, want 2 (both devices)", len(calls))
	}
	gotPIDs := []*actor.PID{calls[0].PID, calls[1].PID}
	wantPIDs := []*actor.PID{pidA, pidB}
	if !reflect.DeepEqual(gotPIDs, wantPIDs) {
		t.Errorf("PIDs: got %v, want %v", gotPIDs, wantPIDs)
	}
	for i, c := range calls {
		if c.Msg != proto.Message(fwd) {
			t.Errorf("calls[%d].Msg: got %p, want %p", i, c.Msg, fwd)
		}
	}
}

// --- Logging compliance --------------------------------------------------

func TestGrain_DomainLogsCarryGrainTypeAndOutcome(t *testing.T) {
	buf := captureLogs(t)
	h := newGrain(t)
	mustRegister(t, h, actor.NewPID("addr", "conn-1"))

	out := buf.String()
	if !strings.Contains(out, `msg=user.connection.registered`) {
		t.Errorf("logs missing user.connection.registered: %s", out)
	}
	if !strings.Contains(out, `grain_type=UserGrain`) {
		t.Errorf("logs missing grain_type=UserGrain: %s", out)
	}
	if !strings.Contains(out, `pid_address=addr`) || !strings.Contains(out, `pid_id=conn-1`) {
		t.Errorf("logs missing pid_address/pid_id: %s", out)
	}
}

func TestGrain_DoesNotLogMessageText(t *testing.T) {
	t.Run("SendMessage logs text_len but not text", func(t *testing.T) {
		const text = "secret-payload"
		buf := captureLogs(t)
		h := newGrain(t)

		_, _ = h.g.SendMessage(&userpb.SendMessageRequest{RoomId: "4", Text: text}, fakeUserCtx("1"))

		out := buf.String()
		if strings.Contains(out, text) {
			t.Errorf("SendMessage log leaked text body: %s", out)
		}
		wantLen := fmt.Sprintf("text_len=%d", len(text))
		if !strings.Contains(out, wantLen) {
			t.Errorf("SendMessage log missing %s: %s", wantLen, out)
		}
	})

	t.Run("ForwardMessage logs text_len but not text", func(t *testing.T) {
		const text = "secret-payload"
		buf := captureLogs(t)
		h := newGrain(t)

		_, _ = h.g.ForwardMessage(&userpb.ForwardMessageRequest{
			Room: &commonpb.RoomRef{RoomId: "4"}, Sender: &commonpb.UserRef{Id: "1", Name: "Alice Example"}, Text: text, Timestamp: timestamppb.New(time.UnixMilli(1)),
		}, fakeUserCtx("1"))

		out := buf.String()
		if strings.Contains(out, text) {
			t.Errorf("ForwardMessage log leaked text body: %s", out)
		}
		wantLen := fmt.Sprintf("text_len=%d", len(text))
		if !strings.Contains(out, wantLen) {
			t.Errorf("ForwardMessage log missing %s: %s", wantLen, out)
		}
	})
}

// --- Lifecycle / boilerplate --------------------------------------------

// Note: lifecycle logs (grain.activated / grain.passivated) are emitted by
// the receiver middleware, not the grain body. See
// internal/middleware/logging_test.go for those assertions.

func TestGrain_ReceiveDefault_LogsUnhandled(t *testing.T) {
	buf := captureLogs(t)
	h := newGrain(t)
	h.g.ReceiveDefault(fakeUserCtx("1", graintest.WithMessage(struct{ X int }{X: 1})))

	if !strings.Contains(buf.String(), "grain.unhandled") {
		t.Errorf("ReceiveDefault did not emit grain.unhandled log: %s", buf.String())
	}
}

func TestGrain_NewKind_ReturnsRegisteredKind(t *testing.T) {
	if k := user.NewKind(nil); k == nil {
		t.Fatal("NewKind: got nil, want non-nil *cluster.Kind")
	}
}

// resolveDirStub is a user.Directory whose Resolve result is fully configured
// by the test: a fixed UserRef on success, or a non-nil error to exercise the
// directory-miss fallback.
type resolveDirStub struct {
	ref id.UserRef
	err error
}

func (d resolveDirStub) Resolve(context.Context, id.UserID) (id.UserRef, error) {
	return d.ref, d.err
}

// TestGrain_ResolveSelf_SeedsNameAndDegradesGracefully exercises the three
// fallback branches of self-resolution at activation. The invariant under
// test: Init always leaves a non-nil self UserRef so command routing can never
// deref a nil sender — a directory miss or an unparseable identity degrades to
// showing the raw id rather than breaking message flow, and each degradation
// emits the seed-failure warning.
func TestGrain_ResolveSelf_SeedsNameAndDegradesGracefully(t *testing.T) {
	aliceID, err := id.NewUserID(1)
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	aliceCode, err := id.ParsePublicCode("A000000001")
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	seededRef, err := id.NewUserRef(aliceID, aliceCode, "Alice Display")
	if err != nil {
		t.Fatalf("NewUserRef: %v", err)
	}

	tests := []struct {
		name      string
		identity  string
		directory user.Directory // nil means no directory injected
		wantID    string
		wantName  string
		// wantPublicCode is the bare public code Self carries. It is present
		// only on a directory hit; every degrade path leaves it empty so the
		// internal id can never stand in for it on the client wire.
		wantPublicCode string
		wantReason     string // seed-failure reason expected in logs; "" means no warning
	}{
		{
			name:     "no directory falls back to identity as name, no public code",
			identity: "1",
			wantID:   "1",
			wantName: "1",
		},
		{
			name:           "directory hit seeds the name and public code",
			identity:       "1",
			directory:      resolveDirStub{ref: seededRef},
			wantID:         "1",
			wantName:       "Alice Display",
			wantPublicCode: "A000000001",
		},
		{
			name:       "directory miss degrades to a code-less self and warns",
			identity:   "1",
			directory:  resolveDirStub{err: user.ErrProfileNotFound},
			wantID:     "1",
			wantName:   "1",
			wantReason: "directory_miss",
		},
		{
			name:       "directory backend error degrades to a code-less self and logs the error class",
			identity:   "1",
			directory:  resolveDirStub{err: errors.New("dial tcp: connection refused")},
			wantID:     "1",
			wantName:   "1",
			wantReason: "directory_error",
		},
		{
			name:       "unparseable identity degrades to a code-less self and warns",
			identity:   "bad/identity",
			wantID:     "bad/identity",
			wantName:   "bad/identity",
			wantReason: "invalid_identity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)

			g := &user.Grain{}
			g.SetRoomClient(&fakeRoomClient{})
			if tt.directory != nil {
				g.SetDirectory(tt.directory)
			}
			g.Init(fakeUserCtx(tt.identity))

			self := g.Self()
			if self == nil {
				t.Fatal("Self(): got nil, want a non-nil UserRef (name seeding must never break message flow)")
			}
			if self.GetId() != tt.wantID {
				t.Errorf("Self().Id: got %q, want %q", self.GetId(), tt.wantID)
			}
			if self.GetName() != tt.wantName {
				t.Errorf("Self().Name: got %q, want %q", self.GetName(), tt.wantName)
			}
			if self.GetPublicCode() != tt.wantPublicCode {
				t.Errorf("Self().PublicCode: got %q, want %q", self.GetPublicCode(), tt.wantPublicCode)
			}

			out := logs.String()
			if tt.wantReason == "" {
				if strings.Contains(out, "user.profile.seed_failed") {
					t.Errorf("expected no seed-failure warning, got logs:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, "user.profile.seed_failed") || !strings.Contains(out, tt.wantReason) {
				t.Errorf("expected a seed-failure warning with reason %q, got logs:\n%s", tt.wantReason, out)
			}
		})
	}
}

// --- helpers -------------------------------------------------------------

func mustRegister(t *testing.T, h *grainHarness, pid *actor.PID) {
	t.Helper()
	resp, err := h.g.RegisterConnection(
		pidRegisterReq(pid),
		fakeUserCtx("1", graintest.WithWatchRecorder(h.watcher)),
	)
	if err != nil {
		t.Fatalf("RegisterConnection(%v) unexpected error: %v", pid, err)
	}
	if resp.GetError() != nil {
		t.Fatalf("RegisterConnection(%v) failed: %+v", pid, resp.GetError())
	}
}

// pidRegisterReq builds a RegisterConnectionRequest carrying the actor PID
// the grain expects (address + id), keeping test bodies tabular.
func pidRegisterReq(pid *actor.PID) *userpb.RegisterConnectionRequest {
	return &userpb.RegisterConnectionRequest{
		RequesterPid: &userpb.PID{
			Address: pid.GetAddress(),
			Id:      pid.GetId(),
		},
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// assertErrResponse verifies a grain response's failure shape: the
// ErrorDetail must be populated with the expected code/status and a
// non-empty message. One helper covers every response type now that they
// all share commonpb.ErrorDetail (ADR-013).
func assertErrResponse(t *testing.T, ed *commonpb.ErrorDetail, wantCode int32, wantStatus string) {
	t.Helper()
	if ed == nil {
		t.Fatal("Error: got nil, want populated")
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

// Compile-time guard that fakeRoomClient satisfies the package's roomClient
// interface (exposed as user.RoomClient in export_test.go).
var _ user.RoomClient = (*fakeRoomClient)(nil)
