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
	"github.com/oklahomer/blabby/internal/middleware"
)

// kindName is the canonical grain_type value emitted by every log call from
// this package and passed to middleware.GrainLogging. Pinned at one site so
// the envelope contract cannot drift.
const kindName = "UserGrain"

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
)

// Error code constants mirror the canonical taxonomy in
// internal/gateway/errors.go. They are duplicated as raw values here to
// avoid a dependency from internal/grain → internal/gateway, which would
// invert the architectural direction.
const (
	codeRoomNotMember     int32 = 2001
	codeRoomAlreadyMember int32 = 2002
	codeRoomNotFound      int32 = 2003
	codeInvalidRequest    int32 = 4001
	codeMissingField      int32 = 4002
	codeInternalError     int32 = 5001
)

const (
	statusRoomNotMember     = "ROOM_NOT_MEMBER"
	statusRoomAlreadyMember = "ROOM_ALREADY_MEMBER"
	statusRoomNotFound      = "ROOM_NOT_FOUND"
	statusInvalidRequest    = "INVALID_REQUEST"
	statusMissingField      = "MISSING_FIELD"
	statusInternalError     = "INTERNAL_ERROR"
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
}

// NewKind registers User grain with a proto.actor cluster, returning a
// *cluster.Kind that callers pass to cluster.WithKinds(...). The default
// roomClient is built lazily during Init from ctx.Cluster(), avoiding a
// chicken-and-egg with cluster.New requiring kinds in its config.
func NewKind() *cluster.Kind {
	return userpb.NewUserGrainKind(
		func() userpb.UserGrain {
			return &Grain{}
		},
		passivationTimeout,
		actor.WithReceiverMiddleware(middleware.GrainLogging(kindName)),
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
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"msg_type", "Terminated",
			"pid_address", msg.Who.GetAddress(),
			"pid_id", msg.Who.GetId(),
		)
	default:
		slog.Warn(middleware.EventGrainUnhandled,
			"grain_type", kindName,
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
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"reason", statusInvalidRequest,
		)
		return &userpb.RegisterConnectionResponse{Error: errDetail(codeInvalidRequest, statusInvalidRequest, "requester_pid is required")}, nil
	}
	if requesterPid.GetAddress() == "" || requesterPid.GetId() == "" {
		slog.Warn(eventUserConnectionRegisterRejected,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"pid_address", requesterPid.GetAddress(),
			"pid_id", requesterPid.GetId(),
			"reason", statusInvalidRequest,
		)
		return &userpb.RegisterConnectionResponse{Error: errDetail(codeInvalidRequest, statusInvalidRequest, "requester_pid.address and requester_pid.id are required")}, nil
	}

	pid := &actor.PID{Address: requesterPid.GetAddress(), Id: requesterPid.GetId()}
	g.state.addConnection(pid)
	ctx.Watch(pid)
	slog.Info(eventUserConnectionRegistered,
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"pid_address", pid.GetAddress(),
		"pid_id", pid.GetId(),
	)
	return &userpb.RegisterConnectionResponse{}, nil
}

// JoinRoom routes a join command to the Room grain identified by req.RoomId
// and, on success, records the room in the user's joined set. Business
// errors from the Room grain are copied through into inline error fields.
func (g *Grain) JoinRoom(req *userpb.JoinRoomRequest, ctx cluster.GrainContext) (*userpb.JoinRoomResponse, error) {
	if req.GetRoomId() == "" {
		slog.Warn(eventUserRoomJoinRejected,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", statusInvalidRequest,
		)
		return &userpb.JoinRoomResponse{Error: errDetail(codeInvalidRequest, statusInvalidRequest, "room_id is required")}, nil
	}

	roomResp, err := g.rooms.Join(req.GetRoomId(), &roompb.JoinRequest{UserId: ctx.Identity()})
	if err != nil {
		// Transport failures are translated into a structured business
		// error so the gateway treats them uniformly with domain failures.
		// The message stays generic — no actor paths leaked to the client.
		slog.Warn(middleware.EventGrainTransportError,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"msg_type", "JoinRoom",
			"room_id", req.GetRoomId(),
			"error", err,
		)
		return &userpb.JoinRoomResponse{Error: errDetail(codeInternalError, statusInternalError, "failed to reach room")}, nil
	}
	if roomResp.GetError() != nil {
		return &userpb.JoinRoomResponse{Error: roomResp.GetError()}, nil
	}

	g.state.joinRoom(req.GetRoomId())
	slog.Info(eventUserRoomJoined,
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"user_id", ctx.Identity(),
		"room_id", req.GetRoomId(),
	)
	return &userpb.JoinRoomResponse{}, nil
}

// LeaveRoom mirrors JoinRoom: routes to the Room grain and, on success,
// removes the room from the user's joined set.
func (g *Grain) LeaveRoom(req *userpb.LeaveRoomRequest, ctx cluster.GrainContext) (*userpb.LeaveRoomResponse, error) {
	if req.GetRoomId() == "" {
		slog.Warn(eventUserRoomLeaveRejected,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", statusInvalidRequest,
		)
		return &userpb.LeaveRoomResponse{Error: errDetail(codeInvalidRequest, statusInvalidRequest, "room_id is required")}, nil
	}

	roomResp, err := g.rooms.Leave(req.GetRoomId(), &roompb.LeaveRequest{UserId: ctx.Identity()})
	if err != nil {
		slog.Warn(middleware.EventGrainTransportError,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"msg_type", "LeaveRoom",
			"room_id", req.GetRoomId(),
			"error", err,
		)
		return &userpb.LeaveRoomResponse{Error: errDetail(codeInternalError, statusInternalError, "failed to reach room")}, nil
	}
	if roomResp.GetError() != nil {
		return &userpb.LeaveRoomResponse{Error: roomResp.GetError()}, nil
	}

	g.state.leaveRoom(req.GetRoomId())
	slog.Info(eventUserRoomLeft,
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"user_id", ctx.Identity(),
		"room_id", req.GetRoomId(),
	)
	return &userpb.LeaveRoomResponse{}, nil
}

// SendMessage routes a chat-message command to the Room grain. The User
// grain does NOT echo the message locally — multi-device echo is realized
// via the Room grain's fan-out call to ForwardMessage.
func (g *Grain) SendMessage(req *userpb.SendMessageRequest, ctx cluster.GrainContext) (*userpb.SendMessageResponse, error) {
	if req.GetRoomId() == "" {
		slog.Warn(eventUserMessageSendRejected,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"reason", statusInvalidRequest,
		)
		return &userpb.SendMessageResponse{Error: errDetail(codeInvalidRequest, statusInvalidRequest, "room_id is required")}, nil
	}
	if strings.TrimSpace(req.GetText()) == "" {
		slog.Warn(eventUserMessageSendRejected,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"user_id", ctx.Identity(),
			"room_id", req.GetRoomId(),
			"text_len", len(req.GetText()),
			"reason", statusMissingField,
		)
		return &userpb.SendMessageResponse{Error: errDetail(codeMissingField, statusMissingField, "text is required")}, nil
	}

	roomResp, err := g.rooms.PostMessage(req.GetRoomId(), &roompb.PostMessageRequest{
		UserId: ctx.Identity(),
		Text:   req.GetText(),
	})
	if err != nil {
		slog.Warn(middleware.EventGrainTransportError,
			"grain_type", kindName,
			"grain_id", ctx.Identity(),
			"msg_type", "SendMessage",
			"room_id", req.GetRoomId(),
			"error", err,
		)
		return &userpb.SendMessageResponse{Error: errDetail(codeInternalError, statusInternalError, "failed to reach room")}, nil
	}
	if roomResp.GetError() != nil {
		return &userpb.SendMessageResponse{Error: roomResp.GetError()}, nil
	}

	slog.Info(eventUserMessageSent,
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"user_id", ctx.Identity(),
		"room_id", req.GetRoomId(),
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
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"msg_type", "ForwardMessage.fanout",
		"sender_id", req.GetSenderId(),
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
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"msg_type", "NotifyRoomEvent.fanout",
		"room_id", req.GetRoomId(),
		"event_user_id", req.GetUserId(),
		"event_type", req.GetEventType().String(),
		"target_count", len(pids),
	)
	g.fanOut(ctx, pids, req)
	return &userpb.NotifyRoomEventResponse{}, nil
}

// GetJoinedRooms returns a sorted snapshot of the rooms this user has
// joined. The list is sorted by room_id for deterministic output.
func (g *Grain) GetJoinedRooms(_ *userpb.GetJoinedRoomsRequest, ctx cluster.GrainContext) (*userpb.GetJoinedRoomsResponse, error) {
	roomIDs := g.state.joinedRoomIDs()
	slog.Info(eventUserRoomsQueried,
		"grain_type", kindName,
		"grain_id", ctx.Identity(),
		"room_count", len(roomIDs),
	)
	return &userpb.GetJoinedRoomsResponse{RoomIds: roomIDs}, nil
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

// errDetail builds the canonical business-error carrier shared by every
// grain response in this package. See ADR-013.
func errDetail(code int32, status, msg string) *commonpb.ErrorDetail {
	return &commonpb.ErrorDetail{Code: code, Status: status, Message: msg}
}
