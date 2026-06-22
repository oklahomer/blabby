// Package user implements the User grain — a virtual actor that owns a
// chat user's connection set and routes the user's commands into Room
// grains.
//
// Per the project's naming convention, the externally visible type is
// [user.Grain]; do NOT rename it to UserGrain (that would stutter with
// the package name and trip golangci-lint's revive rule).
//
// The grain keeps its connection set and joined-rooms set in memory only.
// When the grain passivates, both sets are dropped and rebuilt as the
// user's actors re-register and the user's next command arrives.
package user

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/proto"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/middleware"
)

// Event-name constants for every log line this package emits. Follow the
// N3 past-tense action-verb convention: <package>.<object>.<past-verb> for
// happy paths, <package>.<object>.<base-verb>_rejected for validation
// refusals. Cross-cutting events (grain.fanout, grain.transport.error,
// etc.) live in internal/middleware as exported constants and are
// referenced via middleware.EventXxx.
const (
	eventUserConnectionRegistered       = "user.connection.registered"
	eventUserConnectionRegisterRejected = "user.connection.register_rejected"
	eventUserRoomJoined                 = "user.room.joined"
	eventUserRoomJoinRejected           = "user.room.join_rejected"
	eventUserRoomLeft                   = "user.room.left"
	eventUserRoomLeaveRejected          = "user.room.leave_rejected"
	eventUserMessageSent                = "user.message.sent"
	eventUserMessageSendRejected        = "user.message.send_rejected"
	eventUserRoomsQueried               = "user.rooms.queried"
	eventUserProfileSeedFailed          = "user.profile.seed_failed"
	eventUserRoomResponseInvalid        = "user.room.response_invalid"
)

// passivationTimeout is the receive-timeout the User kind passivates on.
// User grain is connection-bearing; passivating too eagerly drops the
// connection registrations that have to be re-built on the next message.
// A shorter timeout than Room grain is appropriate because the User grain
// holds no message history.
const passivationTimeout = 2 * time.Minute

// Grain is the in-memory implementation of the User virtual actor.
//
// Field mutation happens directly under the actor model's single-threaded
// guarantee; the project's global immutability rule does not apply to
// grain state.
//
// rooms abstracts cluster calls into Room grains so unit tests can run
// without a cluster. send abstracts ctx.Send into individual UserConnection
// PIDs for the same reason; in production it is left zero and each fan-out
// method falls back to a closure over the active ctx.
type Grain struct {
	state userState
	rooms roomClient
	send  pidSender

	// directory resolves this user's profile on activation. Left nil in tests
	// that do not exercise name seeding; production injects it via NewKind.
	// self is the resolved UserRef (id + display name), built once and reused
	// (read-only) on every command this grain routes to a Room grain.
	directory Directory
	self      *commonpb.UserRef
}

// Directory resolves a user's profile (a UserRef of id + display name) from
// their identity. The User grain seeds its UserRef from it on activation; the
// production implementation is the auth user store. A one-method interface so
// the grain unit-tests can inject a fake (or omit it and fall back to the raw
// identity).
//
// Cluster note: the Phase-1 implementation is a per-node, identically-seeded
// in-memory store. It is coherent across nodes only because that data never
// changes; a mutable directory (renames, persistence) must be backed by a
// shared source so every node resolves the same profile.
type Directory interface {
	Resolve(userID id.UserID) (id.UserRef, error)
}

// NewKind registers User grain with a proto.actor cluster, returning a
// *cluster.Kind that callers pass to cluster.WithKinds(...). The dir argument
// is the directory each activation seeds its display name from. The default
// roomClient is built lazily during Init from ctx.Cluster(), avoiding a
// chicken-and-egg with cluster.New requiring kinds in its config.
func NewKind(dir Directory) *cluster.Kind {
	return userpb.NewUserGrainKind(
		func() userpb.UserGrain {
			return &Grain{directory: dir}
		},
		passivationTimeout,
		actor.WithReceiverMiddleware(middleware.GrainLogging("UserGrain")),
	)
}

// Init prepares an empty state for a freshly activated User grain. When the
// grain was constructed without an injected roomClient (the production path),
// a clusterRoomClient is built from ctx.Cluster() so command routing reaches
// the real Room grain.
func (g *Grain) Init(ctx cluster.GrainContext) {
	g.state = newUserState()
	if g.rooms == nil {
		g.rooms = newClusterRoomClient(ctx.Cluster())
	}
	g.self = g.resolveSelf(ctx)
}

// resolveSelf builds the grain's own UserRef once at activation, seeding the
// display name from the directory. It falls back to the raw identity as the
// name when no directory is injected, the identity is unparseable, or the
// directory has no entry — a miss degrades to showing the user ID rather than
// breaking message flow. The cached result is reused read-only on every
// outbound command.
func (g *Grain) resolveSelf(ctx cluster.GrainContext) *commonpb.UserRef {
	identity := ctx.Identity()
	uid, err := id.ParseUserID(identity)
	if err != nil {
		slog.Warn(eventUserProfileSeedFailed,
			"grain_type", ctx.Kind(), "grain_id", identity, "reason", "invalid_identity")
		return &commonpb.UserRef{Id: identity, Name: identity}
	}
	if g.directory != nil {
		if ref, err := g.directory.Resolve(uid); err == nil {
			return &commonpb.UserRef{Id: ref.ID().String(), Name: ref.Name()}
		}
		slog.Warn(eventUserProfileSeedFailed,
			"grain_type", ctx.Kind(), "grain_id", identity, "reason", "directory_miss")
	}
	return &commonpb.UserRef{Id: identity, Name: identity}
}

// Terminate is a passivation hook; state is not persisted across activations.
func (g *Grain) Terminate(_ cluster.GrainContext) {}

// ReceiveDefault handles non-RPC messages routed to the grain's mailbox.
// The protoactor death-watch delivers *actor.Terminated here when a watched
// UserConnection PID stops; we evict the matching entry so subsequent
// fan-outs never target a dead actor. Anything else is logged and dropped.
func (g *Grain) ReceiveDefault(ctx cluster.GrainContext) {
	switch msg := ctx.Message().(type) {
	case *actor.Terminated:
		g.state.removeConnection(msg.Who)
		slog.Info(middleware.EventGrainConnectionTerminated,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"msg_type", "Terminated",
			"pid_address", msg.Who.GetAddress(),
			"pid_id", msg.Who.GetId(),
		)
	default:
		slog.Warn(middleware.EventGrainUnhandled,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"msg_type", fmt.Sprintf("%T", msg),
		)
	}
}

// RegisterConnection records the caller's persistent PID and arranges for
// automatic eviction when that actor stops. The PID is reconstructed from
// the request's requester_pid field — NOT captured from `ctx.Sender()`,
// which on a cluster RPC is a transient future PID that dead-letters once
// the response is delivered (verified empirically by
// TestUserGrain_SenderPID; see ADR-011).
//
// After storing the PID the grain calls ctx.Watch(pid). When the
// UserConnection actor stops (client disconnect, panic, node loss), the
// resulting Terminated message arrives at ReceiveDefault and the entry is
// evicted. There is no Deregister RPC — see ADR-012.
func (g *Grain) RegisterConnection(req *userpb.RegisterConnectionRequest, ctx cluster.GrainContext) (*userpb.RegisterConnectionResponse, error) {
	requesterPid := req.GetRequesterPid()
	if requesterPid == nil {
		slog.Warn(eventUserConnectionRegisterRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.RegisterConnectionResponse{Error: errDetail(errcode.InvalidRequest, "requester_pid is required")}, nil
	}
	if requesterPid.GetAddress() == "" || requesterPid.GetId() == "" {
		slog.Warn(eventUserConnectionRegisterRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"pid_address", requesterPid.GetAddress(),
			"pid_id", requesterPid.GetId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.RegisterConnectionResponse{Error: errDetail(errcode.InvalidRequest, "requester_pid.address and requester_pid.id are required")}, nil
	}

	pid := &actor.PID{Address: requesterPid.GetAddress(), Id: requesterPid.GetId()}
	g.state.addConnection(pid)
	ctx.Watch(pid)
	slog.Info(eventUserConnectionRegistered,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"pid_address", pid.GetAddress(),
		"pid_id", pid.GetId(),
	)
	return &userpb.RegisterConnectionResponse{}, nil
}

// JoinRoom routes a join command to the Room grain identified by req.RoomId
// and, on success, records the room in the user's joined set. Business
// errors from the Room grain are parsed and canonicalized before forwarding.
func (g *Grain) JoinRoom(req *userpb.JoinRoomRequest, ctx cluster.GrainContext) (*userpb.JoinRoomResponse, error) {
	roomID, err := id.ParseRoomID(req.GetRoomId())
	if err != nil {
		slog.Warn(eventUserRoomJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.JoinRoomResponse{Error: errDetail(errcode.InvalidRequest, "room_id is required")}, nil
	}

	roomResp, err := g.rooms.Join(roomID, &roompb.JoinRequest{User: g.self})
	if err != nil {
		// Transport failures are translated into a structured business
		// error so the gateway treats them uniformly with domain failures.
		// The message stays generic — no actor paths leaked to the client.
		slog.Warn(middleware.EventGrainTransportError,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"msg_type", "JoinRoom",
			"room_id", roomID,
			"error", err,
		)
		return &userpb.JoinRoomResponse{Error: errDetail(errcode.InternalError, "failed to reach room")}, nil
	}
	if roomErr := roomResp.GetError(); roomErr != nil {
		code, detail := parseRoomError(ctx, "JoinRoom", roomID, roomErr)
		// Room keeps its action-oriented contract and reports an existing
		// membership as ROOM_ALREADY_MEMBER. For the HTTP PUT resource contract,
		// that outcome confirms the desired state; recording it here also repairs
		// this User grain after an earlier Room response was lost.
		if code != errcode.RoomAlreadyMember {
			return &userpb.JoinRoomResponse{Error: detail}, nil
		}
	}

	// A loaded Room grain returns its RoomRef on both the success and
	// already-member outcomes, so the User grain caches the room's public code
	// and display name without a separate lookup. A missing or malformed ref is a
	// backend contract breach: fail closed rather than cache a degraded entry; a
	// later retry reconciles via the already-member repair path.
	roomRef, err := parseRoomRef(roomResp.GetRoom())
	if err != nil {
		slog.Error(eventUserRoomResponseInvalid,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"operation", "JoinRoom",
			"room_id", roomID,
			"error", err,
		)
		return &userpb.JoinRoomResponse{Error: errDetail(errcode.InternalError, "room operation failed")}, nil
	}

	g.state.joinRoom(roomRef)
	slog.Info(eventUserRoomJoined,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", ctx.Identity(),
		"room_id", roomID,
	)
	return &userpb.JoinRoomResponse{}, nil
}

// LeaveRoom mirrors JoinRoom: routes to the Room grain and, on success,
// removes the room from the user's joined set.
func (g *Grain) LeaveRoom(req *userpb.LeaveRoomRequest, ctx cluster.GrainContext) (*userpb.LeaveRoomResponse, error) {
	roomID, err := id.ParseRoomID(req.GetRoomId())
	if err != nil {
		slog.Warn(eventUserRoomLeaveRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.LeaveRoomResponse{Error: errDetail(errcode.InvalidRequest, "room_id is required")}, nil
	}

	roomResp, err := g.rooms.Leave(roomID, &roompb.LeaveRequest{UserId: ctx.Identity()})
	if err != nil {
		slog.Warn(middleware.EventGrainTransportError,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"msg_type", "LeaveRoom",
			"room_id", roomID,
			"error", err,
		)
		return &userpb.LeaveRoomResponse{Error: errDetail(errcode.InternalError, "failed to reach room")}, nil
	}
	if roomErr := roomResp.GetError(); roomErr != nil {
		code, detail := parseRoomError(ctx, "LeaveRoom", roomID, roomErr)
		// DELETE has the symmetric rule: ROOM_NOT_MEMBER confirms absence. Apply
		// the local removal even when Room made no change so a retry reconciles
		// the User grain's joined-room projection.
		if code != errcode.RoomNotMember {
			return &userpb.LeaveRoomResponse{Error: detail}, nil
		}
	}

	g.state.leaveRoom(roomID)
	slog.Info(eventUserRoomLeft,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", ctx.Identity(),
		"room_id", roomID,
	)
	return &userpb.LeaveRoomResponse{}, nil
}

// SendMessage routes a chat-message command to the Room grain. The User
// grain does NOT echo the message locally — multi-device echo is realized
// via the Room grain's fan-out call to ForwardMessage.
func (g *Grain) SendMessage(req *userpb.SendMessageRequest, ctx cluster.GrainContext) (*userpb.SendMessageResponse, error) {
	roomID, err := id.ParseRoomID(req.GetRoomId())
	if err != nil {
		slog.Warn(eventUserMessageSendRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return &userpb.SendMessageResponse{Error: errDetail(errcode.InvalidRequest, "room_id is required")}, nil
	}
	if strings.TrimSpace(req.GetText()) == "" {
		slog.Warn(eventUserMessageSendRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", roomID,
			"text_len", len(req.GetText()),
			"reason", errcode.MissingField.Status(),
		)
		return &userpb.SendMessageResponse{Error: errDetail(errcode.MissingField, "text is required")}, nil
	}

	roomResp, err := g.rooms.PostMessage(roomID, &roompb.PostMessageRequest{
		User: g.self,
		Text: req.GetText(),
	})
	if err != nil {
		slog.Warn(middleware.EventGrainTransportError,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"msg_type", "SendMessage",
			"room_id", roomID,
			"error", err,
		)
		return &userpb.SendMessageResponse{Error: errDetail(errcode.InternalError, "failed to reach room")}, nil
	}
	if roomErr := roomResp.GetError(); roomErr != nil {
		_, detail := parseRoomError(ctx, "SendMessage", roomID, roomErr)
		return &userpb.SendMessageResponse{Error: detail}, nil
	}

	slog.Info(eventUserMessageSent,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", ctx.Identity(),
		"room_id", roomID,
		"text_len", len(req.GetText()),
	)
	return &userpb.SendMessageResponse{Timestamp: roomResp.GetTimestamp()}, nil
}

// ForwardMessage receives a chat-message fan-out from a Room grain and
// re-fans it out to every UserConnection registered under this user.
//
// Contract: the message delivered to UserConnection actors is the same
// *userpb.ForwardMessageRequest received here, passed through unchanged.
// The UserConnection actor type-switches on this proto type to format
// JSON for the WebSocket. Do not introduce a new message shape without
// updating the connection actor's switch.
func (g *Grain) ForwardMessage(req *userpb.ForwardMessageRequest, ctx cluster.GrainContext) (*userpb.ForwardMessageResponse, error) {
	pids := g.state.connectionPIDs()
	slog.Info(middleware.EventGrainFanout,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"msg_type", "ForwardMessage.fanout",
		"sender_id", req.GetSender().GetId(),
		"room_id", req.GetRoomId(),
		"target_count", len(pids),
		"text_len", len(req.GetText()),
	)
	g.fanOut(ctx, pids, req)
	return &userpb.ForwardMessageResponse{}, nil
}

// NotifyRoomEvent receives a room membership event from a Room grain and
// re-fans it out to every UserConnection registered under this user.
//
// Contract: same as ForwardMessage — the inbound proto is passed through
// unchanged. The User grain does NOT mutate joined_rooms in response to
// NotifyRoomEvent; joined_rooms is updated only by JoinRoom/LeaveRoom so
// the user's own command outcomes remain the single source of truth.
func (g *Grain) NotifyRoomEvent(req *userpb.NotifyRoomEventRequest, ctx cluster.GrainContext) (*userpb.NotifyRoomEventResponse, error) {
	pids := g.state.connectionPIDs()
	slog.Info(middleware.EventGrainFanout,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"msg_type", "NotifyRoomEvent.fanout",
		"room_id", req.GetRoomId(),
		"event_user_id", req.GetUser().GetId(),
		"event_type", req.GetEventType().String(),
		"target_count", len(pids),
	)
	g.fanOut(ctx, pids, req)
	return &userpb.NotifyRoomEventResponse{}, nil
}

// GetJoinedRooms returns the rooms this user has joined as reference metadata
// (public code, display name, status), sorted by room id for deterministic
// output. The gateway renders these directly, so it no longer queries the room
// repository per request.
func (g *Grain) GetJoinedRooms(_ *userpb.GetJoinedRoomsRequest, ctx cluster.GrainContext) (*userpb.GetJoinedRoomsResponse, error) {
	refs := g.state.joinedRoomRefs()
	slog.Info(eventUserRoomsQueried,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"room_count", len(refs),
	)
	rooms := make([]*commonpb.RoomRef, len(refs))
	for i, ref := range refs {
		rooms[i] = protoRoomRef(ref)
	}
	return &userpb.GetJoinedRoomsResponse{Rooms: rooms}, nil
}

// fanOut delivers msg to each PID using either the test-injected sender
// (g.send) or a fallback closure over ctx.Send.
//
// The fallback is built per call rather than stored on the Grain to keep
// the production path stateless and the test-injection path obvious — a
// preset g.send always wins, no field reassignment in handlers, no
// captured ctx escaping its handler scope.
func (g *Grain) fanOut(ctx cluster.GrainContext, pids []*actor.PID, msg proto.Message) {
	send := g.send
	if send == nil {
		send = func(pid *actor.PID, m proto.Message) { ctx.Send(pid, m) }
	}
	for _, pid := range pids {
		send(pid, msg)
	}
}

// parseRoomError converts a raw Room-grain error into the shared taxonomy and
// rebuilds its wire representation from the parsed code. Invalid pairs fail
// closed as a client-safe internal error.
func parseRoomError(ctx cluster.GrainContext, operation string, roomID id.RoomID, detail *commonpb.ErrorDetail) (errcode.Code, *commonpb.ErrorDetail) {
	code, err := errcode.Parse(detail.GetCode(), detail.GetStatus())
	if err != nil {
		slog.Error(eventUserRoomResponseInvalid,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"operation", operation,
			"room_id", roomID,
			"code", detail.GetCode(),
			"status", detail.GetStatus(),
			"error", err,
		)
		return errcode.InternalError, errDetail(errcode.InternalError, "room operation failed")
	}
	return code, errDetail(code, detail.GetMessage())
}

// parseRoomRef parses the proto RoomRef carried on a Join response into a typed
// domain.RoomRef at the grain boundary (parse, don't validate). A nil ref, an
// invalid id or public code, or an unknown status is rejected so the caller can
// fail closed rather than cache a degraded entry.
func parseRoomRef(p *commonpb.RoomRef) (domain.RoomRef, error) {
	roomID, err := id.ParseRoomID(p.GetRoomId())
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("room_id: %w", err)
	}
	code, err := id.ParsePublicCode(p.GetPublicCode())
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("public_code: %w", err)
	}
	status, err := domain.ParseRoomStatus(p.GetStatus())
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("status: %w", err)
	}
	return domain.RoomRef{
		ID:              roomID,
		PublicCode:      code,
		Name:            p.GetName(),
		Status:          status,
		MetadataVersion: p.GetMetadataVersion(),
	}, nil
}

// protoRoomRef renders a cached domain.RoomRef back onto the wire for
// GetJoinedRooms. The gateway prefixes the public code (R…) for clients.
func protoRoomRef(r domain.RoomRef) *commonpb.RoomRef {
	return &commonpb.RoomRef{
		RoomId:          r.ID.String(),
		PublicCode:      r.PublicCode.String(),
		Name:            r.Name,
		Status:          string(r.Status),
		MetadataVersion: r.MetadataVersion,
	}
}

// errDetail builds the canonical business-error carrier shared by every
// grain response in this package. See ADR-013.
func errDetail(code errcode.Code, msg string) *commonpb.ErrorDetail {
	return &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg}
}
