// Package room implements the Room grain — a virtual actor that owns chat
// room membership and the canonical post-message → fan-out pipeline.
//
// Per the project's naming convention, the externally visible type is
// [room.Grain]; do NOT rename it to RoomGrain (that would stutter with the
// package name and trip golangci-lint's revive rule).
//
// Phase 1 keeps room state in memory and uses best-effort fan-out; persistence
// and retry policy are deferred to later phases.
package room

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/middleware"
)

// Event-name constants for every log line this package emits. Follow the
// N3 past-tense action-verb convention: <package>.<object>.<past-verb> for
// happy paths, <package>.<object>.<base-verb>_rejected for validation
// refusals. Cross-cutting events (grain.fanout, grain.fanout.error,
// grain.unhandled) live in internal/middleware as exported constants and
// are referenced via middleware.EventXxx.
const (
	eventRoomMemberJoined        = "room.member.joined"
	eventRoomMemberJoinRejected  = "room.member.join_rejected"
	eventRoomMemberLeft          = "room.member.left"
	eventRoomMemberLeaveRejected = "room.member.leave_rejected"
	eventRoomMessagePosted       = "room.message.posted"
	eventRoomMessagePostRejected = "room.message.post_rejected"
	eventRoomFanoutSupervision   = "room.fanout.supervision"
)

// passivationTimeout is the receive-timeout the Room kind passivates on. It is
// longer than the User grain's because the Room holds the recent-message buffer.
const passivationTimeout = 5 * time.Minute

// userNotifier abstracts the User grain client surface used for fan-out so
// the Room grain can be unit-tested without a real cluster.
type userNotifier interface {
	NotifyRoomEvent(userID id.UserID, req *userpb.NotifyRoomEventRequest) error
	ForwardMessage(userID id.UserID, req *userpb.ForwardMessageRequest) error
}

// clusterUserNotifier is the production userNotifier; it routes calls to the
// real User grain via the generated grain client.
type clusterUserNotifier struct {
	c *cluster.Cluster
}

func (n *clusterUserNotifier) NotifyRoomEvent(userID id.UserID, req *userpb.NotifyRoomEventRequest) error {
	if _, err := userpb.GetUserGrainGrainClient(n.c, userID.String()).NotifyRoomEvent(req); err != nil {
		return fmt.Errorf("user grain NotifyRoomEvent: %w", err)
	}
	return nil
}

func (n *clusterUserNotifier) ForwardMessage(userID id.UserID, req *userpb.ForwardMessageRequest) error {
	if _, err := userpb.GetUserGrainGrainClient(n.c, userID.String()).ForwardMessage(req); err != nil {
		return fmt.Errorf("user grain ForwardMessage: %w", err)
	}
	return nil
}

// Grain is the in-memory implementation of the Room virtual actor.
//
// Field mutation happens directly under the actor model's single-threaded
// guarantee; the project's global immutability rule does not apply to grain
// state.
//
// The clock seam (`now`) returns a domain time.Time for readability and
// type safety; the conversion to google.protobuf.Timestamp happens at the
// proto boundaries (PostMessage's response and buildForwardMessage in
// events.go).
type Grain struct {
	state    roomState
	notifier userNotifier
	now      func() time.Time
	fanout   fanoutDispatcher
}

// roomGrainKind is the cluster kind name registered by the generated
// NewRoomGrainKind. The log envelopes below must agree with it.
const roomGrainKind = "RoomGrain"

// NewKind registers Room grain with a proto.actor cluster, returning a
// *cluster.Kind that callers pass to cluster.WithKinds(...). The default
// userNotifier is built lazily during Init from ctx.Cluster(), avoiding a
// chicken-and-egg with cluster.New requiring kinds in its config.
func NewKind() *cluster.Kind {
	return roompb.NewRoomGrainKind(func() roompb.RoomGrain { return &Grain{} }, passivationTimeout,
		actor.WithReceiverMiddleware(middleware.GrainLogging(roomGrainKind)),
		// Govern the fan-out worker child: protoactor resolves a child's
		// supervisor from its parent's props. Unexpected worker panics restart
		// the stateless actor instance while preserving its PID and remaining
		// mailbox; see supervision.go.
		actor.WithSupervisor(newFanoutSupervisor(nil)),
	)
}

// Init prepares an empty state for a freshly activated Room grain. When the
// grain was constructed without an injected notifier (the production path),
// a clusterUserNotifier is built from ctx.Cluster() so fan-out reaches the
// real User grain.
func (g *Grain) Init(ctx cluster.GrainContext) {
	g.state = newRoomState()
	if g.now == nil {
		g.now = time.Now
	}
	if g.notifier == nil {
		g.notifier = &clusterUserNotifier{c: ctx.Cluster()}
	}
	if g.fanout == nil {
		// Member fan-out runs on a child actor so command handlers return
		// without blocking on (and re-entering via) the per-member notification
		// RPCs (ADR-015). The child stops with the grain and restarts in place
		// after an unexpected panic.
		pid := ctx.Spawn(actor.PropsFromProducer(func() actor.Actor {
			return &fanoutWorker{notifier: g.notifier}
		}))
		g.fanout = &actorDispatcher{pid: pid}
	}
}

// Terminate is a passivation hook; Phase 1 does not persist state.
func (g *Grain) Terminate(_ cluster.GrainContext) {}

// ReceiveDefault logs unexpected messages without crashing the grain.
func (g *Grain) ReceiveDefault(ctx cluster.GrainContext) {
	slog.Warn(middleware.EventGrainUnhandled,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"msg_type", fmt.Sprintf("%T", ctx.Message()),
	)
}

// Join adds the user to the room and fans out a JOINED event to every
// current member (including the joiner — multi-device echo).
func (g *Grain) Join(req *roompb.JoinRequest, ctx cluster.GrainContext) (*roompb.JoinResponse, error) {
	joiner, err := parseUserRef(req.GetUser())
	if err != nil {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", req.GetUser().GetId(),
			"reason", errcode.InvalidRequest.Status(),
			"error", err,
		)
		return joinErr(errcode.InvalidRequest, "user id and display name are required"), nil
	}
	userID := joiner.ID()
	if g.state.isMember(userID) {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", errcode.RoomAlreadyMember.Status(),
		)
		return joinErr(errcode.RoomAlreadyMember, "already a member of this room"), nil
	}

	g.state.addMember(joiner)
	recipients := g.state.memberIDs()
	slog.Info(eventRoomMemberJoined,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", userID,
	)
	slog.Info(middleware.EventGrainFanout,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"msg_type", "Join.fanout",
		"sender_id", userID,
		"target_count", len(recipients),
	)
	g.fanOutNotify(ctx, recipients, buildJoinedEvent(ctx.Identity(), joiner), "Join.fanout")

	return &roompb.JoinResponse{}, nil
}

// Leave removes the user from the room and fans out a LEFT event to the
// pre-removal member snapshot (including the leaver, so their connection
// can update UI state symmetrically with Join).
func (g *Grain) Leave(req *roompb.LeaveRequest, ctx cluster.GrainContext) (*roompb.LeaveResponse, error) {
	userID, err := id.NewUserID(req.GetUserId())
	if err != nil {
		slog.Warn(eventRoomMemberLeaveRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", req.GetUserId(),
			"reason", errcode.InvalidRequest.Status(),
		)
		return leaveErr(errcode.InvalidRequest, "user_id is required"), nil
	}
	leaver, ok := g.state.memberRef(userID)
	if !ok {
		slog.Warn(eventRoomMemberLeaveRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", errcode.RoomNotMember.Status(),
		)
		return leaveErr(errcode.RoomNotMember, "not a member of this room"), nil
	}

	// Snapshot before removal so the leaver also receives the LEFT event;
	// leaver carries the cached display name for labeling.
	recipients := g.state.memberIDs()
	g.state.removeMember(userID)
	slog.Info(eventRoomMemberLeft,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", userID,
	)
	slog.Info(middleware.EventGrainFanout,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"msg_type", "Leave.fanout",
		"sender_id", userID,
		"target_count", len(recipients),
	)
	g.fanOutNotify(ctx, recipients, buildLeftEvent(ctx.Identity(), leaver), "Leave.fanout")

	return &roompb.LeaveResponse{}, nil
}

// PostMessage records the message, assigns the server-side timestamp, and
// fans the message out unconditionally to every current member.
func (g *Grain) PostMessage(req *roompb.PostMessageRequest, ctx cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	sender, err := parseUserRef(req.GetUser())
	if err != nil {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", req.GetUser().GetId(),
			"reason", errcode.InvalidRequest.Status(),
			"error", err,
		)
		return postErr(errcode.InvalidRequest, "user id and display name are required"), nil
	}
	userID := sender.ID()
	if strings.TrimSpace(req.GetText()) == "" {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"text_len", len(req.GetText()),
			"reason", errcode.MissingField.Status(),
		)
		return postErr(errcode.MissingField, "text is required"), nil
	}
	if !g.state.isMember(userID) {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", errcode.RoomNotMember.Status(),
		)
		return postErr(errcode.RoomNotMember, "not a member of this room"), nil
	}

	// Refresh the cached name from the value carried on this message, so the
	// room's roster reflects the sender's current display name.
	g.state.refreshMember(sender)

	timestamp := g.now()
	g.state.recordMessage(chatMessage{
		senderID:  userID,
		text:      req.GetText(),
		timestamp: timestamp,
	})

	recipients := g.state.memberIDs()
	textLen := len(req.GetText())
	slog.Info(eventRoomMessagePosted,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"user_id", userID,
	)
	slog.Info(middleware.EventGrainFanout,
		"grain_type", ctx.Kind(),
		"grain_id", ctx.Identity(),
		"msg_type", "PostMessage.fanout",
		"sender_id", userID,
		"target_count", len(recipients),
		"text_len", textLen,
	)
	payload := buildForwardMessage(ctx.Identity(), sender, req.GetText(), timestamp)
	g.fanOutForward(ctx, recipients, payload, "PostMessage.fanout")

	return &roompb.PostMessageResponse{Timestamp: timestamppb.New(timestamp)}, nil
}

// fanOutNotify hands a NotifyRoomEvent fan-out job to the grain's fan-out
// child, which performs the best-effort per-recipient delivery off this
// grain's message goroutine (see ADR-015 and fanout.go). The grain context is
// read here, not in the child: ctx.Kind/ctx.Identity are captured into the
// job because the GrainContext must not be touched outside this handler.
func (g *Grain) fanOutNotify(ctx cluster.GrainContext, recipients []id.UserID, payload *userpb.NotifyRoomEventRequest, msgType string) {
	g.fanout.notify(ctx, &fanoutNotify{
		recipients: recipients,
		payload:    payload,
		msgType:    msgType,
		grainKind:  ctx.Kind(),
		grainID:    ctx.Identity(),
	})
}

// fanOutForward hands a ForwardMessage fan-out job to the grain's fan-out
// child. Same best-effort, off-the-message-goroutine semantics as
// fanOutNotify.
func (g *Grain) fanOutForward(ctx cluster.GrainContext, recipients []id.UserID, payload *userpb.ForwardMessageRequest, msgType string) {
	g.fanout.forward(ctx, &fanoutForward{
		recipients: recipients,
		payload:    payload,
		msgType:    msgType,
		grainKind:  ctx.Kind(),
		grainID:    ctx.Identity(),
	})
}

// parseUserRef parses an inbound proto UserRef into a validated domain
// UserRef at the grain boundary (parse, don't validate). A nil ref, an
// invalid id, or an empty name is rejected so handlers can return
// INVALID_REQUEST.
func parseUserRef(p *commonpb.UserRef) (id.UserRef, error) {
	userID, err := id.NewUserID(p.GetId())
	if err != nil {
		return id.UserRef{}, err
	}
	return id.NewUserRef(userID, p.GetName())
}

func joinErr(code errcode.Code, msg string) *roompb.JoinResponse {
	return &roompb.JoinResponse{
		Error: &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg},
	}
}

func leaveErr(code errcode.Code, msg string) *roompb.LeaveResponse {
	return &roompb.LeaveResponse{
		Error: &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg},
	}
}

func postErr(code errcode.Code, msg string) *roompb.PostMessageResponse {
	return &roompb.PostMessageResponse{
		Error: &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg},
	}
}
