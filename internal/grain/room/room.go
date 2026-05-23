// Package room implements the Room grain — a virtual actor that owns chat
// room membership and the canonical post-message → fan-out pipeline.
//
// Per the project's naming convention, the externally visible type is
// [room.Grain]; do NOT rename it to RoomGrain (that would stutter with the
// package name and trip golangci-lint's revive rule).
//
// Room grain semantics, error codes, and fan-out shape are specified in
// architecture.md and the Story 3.1 spec. Phase 1 keeps state in memory
// and uses best-effort fan-out; persistence and retry policy are deferred
// to later phases.
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
)

// Error code constants mirror the canonical taxonomy in
// internal/gateway/errors.go. They are duplicated as raw values here to
// avoid a dependency from internal/grain → internal/gateway, which would
// invert the architectural direction.
const (
	codeRoomNotMember     int32 = 2001
	codeRoomAlreadyMember int32 = 2002
	codeInvalidRequest    int32 = 4001
	codeMissingField      int32 = 4002
)

const (
	statusRoomNotMember     = "ROOM_NOT_MEMBER"
	statusRoomAlreadyMember = "ROOM_ALREADY_MEMBER"
	statusInvalidRequest    = "INVALID_REQUEST"
	statusMissingField      = "MISSING_FIELD"
)

// passivationTimeout is the receive-timeout the Room kind passivates on.
// Architecture.md RB3 calls for a longer timeout than User grain because
// Room holds the recent-message buffer.
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
// state (architecture.md "Grain State Management").
//
// The clock seam (`now`) returns a domain time.Time for readability and
// type safety; the conversion to google.protobuf.Timestamp happens at the
// proto boundaries (PostMessage's response and buildForwardMessage in
// events.go).
type Grain struct {
	state    roomState
	notifier userNotifier
	now      func() time.Time
}

// NewKind registers Room grain with a proto.actor cluster, returning a
// *cluster.Kind that callers pass to cluster.WithKinds(...). The default
// userNotifier is built lazily during Init from ctx.Cluster(), avoiding a
// chicken-and-egg with cluster.New requiring kinds in its config.
//
// Use this constructor in cmd/server/main.go or in a future cluster bootstrap
// (Story 3.4 / 3.5 wires the production cluster). It is also fine to call
// from integration tests via internal/testutil/cluster.
func NewKind() *cluster.Kind {
	return roompb.NewRoomGrainKind(func() roompb.RoomGrain {
		return &Grain{}
	}, passivationTimeout,
		actor.WithReceiverMiddleware(middleware.GrainLogging("RoomGrain")),
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
// current member (including the joiner — multi-device echo, FR4).
func (g *Grain) Join(req *roompb.JoinRequest, ctx cluster.GrainContext) (*roompb.JoinResponse, error) {
	userID, err := id.NewUserID(req.GetUserId())
	if err != nil {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", req.GetUserId(),
			"reason", statusInvalidRequest,
		)
		return joinErr(codeInvalidRequest, statusInvalidRequest, "user_id is required"), nil
	}
	if g.state.isMember(userID) {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", statusRoomAlreadyMember,
		)
		return joinErr(codeRoomAlreadyMember, statusRoomAlreadyMember, "already a member of this room"), nil
	}

	g.state.addMember(userID)
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
	g.fanOutNotify(ctx, recipients, buildJoinedEvent(ctx.Identity(), userID), "Join.fanout")

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
			"reason", statusInvalidRequest,
		)
		return leaveErr(codeInvalidRequest, statusInvalidRequest, "user_id is required"), nil
	}
	if !g.state.isMember(userID) {
		slog.Warn(eventRoomMemberLeaveRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", statusRoomNotMember,
		)
		return leaveErr(codeRoomNotMember, statusRoomNotMember, "not a member of this room"), nil
	}

	// Snapshot before removal so the leaver also receives the LEFT event.
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
	g.fanOutNotify(ctx, recipients, buildLeftEvent(ctx.Identity(), userID), "Leave.fanout")

	return &roompb.LeaveResponse{}, nil
}

// PostMessage records the message, assigns the server-side timestamp, and
// fans the message out unconditionally to every current member (FR4).
func (g *Grain) PostMessage(req *roompb.PostMessageRequest, ctx cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	userID, err := id.NewUserID(req.GetUserId())
	if err != nil {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", req.GetUserId(),
			"reason", statusInvalidRequest,
		)
		return postErr(codeInvalidRequest, statusInvalidRequest, "user_id is required"), nil
	}
	if strings.TrimSpace(req.GetText()) == "" {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"text_len", len(req.GetText()),
			"reason", statusMissingField,
		)
		return postErr(codeMissingField, statusMissingField, "text is required"), nil
	}
	if !g.state.isMember(userID) {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", statusRoomNotMember,
		)
		return postErr(codeRoomNotMember, statusRoomNotMember, "not a member of this room"), nil
	}

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
	payload := buildForwardMessage(ctx.Identity(), userID, req.GetText(), timestamp)
	g.fanOutForward(ctx, recipients, payload, "PostMessage.fanout")

	return &roompb.PostMessageResponse{Timestamp: timestamppb.New(timestamp)}, nil
}

// fanOutNotify delivers a NotifyRoomEvent to each recipient. Failures are
// logged at warn level but do not abort the operation — Phase 1 fan-out is
// best-effort (architecture.md "Process Patterns: no automatic retries").
//
// Phase 1 uses a sequential loop; concurrency can be added later if measured
// fan-out latency demands it.
func (g *Grain) fanOutNotify(ctx cluster.GrainContext, recipients []id.UserID, payload *userpb.NotifyRoomEventRequest, msgType string) {
	for _, recipientID := range recipients {
		if err := g.notifier.NotifyRoomEvent(recipientID, payload); err != nil {
			slog.Warn(middleware.EventGrainFanoutError,
				"grain_type", ctx.Kind(),
				"grain_id", ctx.Identity(),
				"msg_type", msgType,
				"recipient_id", recipientID,
				"error", err,
			)
		}
	}
}

// fanOutForward delivers a ForwardMessage to each recipient. Same best-effort
// semantics as fanOutNotify.
func (g *Grain) fanOutForward(ctx cluster.GrainContext, recipients []id.UserID, payload *userpb.ForwardMessageRequest, msgType string) {
	for _, recipientID := range recipients {
		if err := g.notifier.ForwardMessage(recipientID, payload); err != nil {
			slog.Warn(middleware.EventGrainFanoutError,
				"grain_type", ctx.Kind(),
				"grain_id", ctx.Identity(),
				"msg_type", msgType,
				"recipient_id", recipientID,
				"error", err,
			)
		}
	}
}

func joinErr(code int32, status, msg string) *roompb.JoinResponse {
	return &roompb.JoinResponse{
		Error: &commonpb.ErrorDetail{Code: code, Status: status, Message: msg},
	}
}

func leaveErr(code int32, status, msg string) *roompb.LeaveResponse {
	return &roompb.LeaveResponse{
		Error: &commonpb.ErrorDetail{Code: code, Status: status, Message: msg},
	}
}

func postErr(code int32, status, msg string) *roompb.PostMessageResponse {
	return &roompb.PostMessageResponse{
		Error: &commonpb.ErrorDetail{Code: code, Status: status, Message: msg},
	}
}
