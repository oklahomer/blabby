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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
	userpb "github.com/oklahomer/blabby/gen/user"
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
	NotifyRoomEvent(userID string, req *userpb.NotifyRoomEventRequest) error
	ForwardMessage(userID string, req *userpb.ForwardMessageRequest) error
}

// clusterUserNotifier is the production userNotifier; it routes calls to the
// real User grain via the generated grain client.
type clusterUserNotifier struct {
	c *cluster.Cluster
}

func (n *clusterUserNotifier) NotifyRoomEvent(userID string, req *userpb.NotifyRoomEventRequest) error {
	resp, err := userpb.GetUserGrainGrainClient(n.c, userID).NotifyRoomEvent(req)
	if err != nil {
		return fmt.Errorf("user grain NotifyRoomEvent: %w", err)
	}
	if resp == nil || !resp.GetSuccess() {
		return errors.New("user grain NotifyRoomEvent reported failure")
	}
	return nil
}

func (n *clusterUserNotifier) ForwardMessage(userID string, req *userpb.ForwardMessageRequest) error {
	resp, err := userpb.GetUserGrainGrainClient(n.c, userID).ForwardMessage(req)
	if err != nil {
		return fmt.Errorf("user grain ForwardMessage: %w", err)
	}
	if resp == nil || !resp.GetSuccess() {
		return errors.New("user grain ForwardMessage reported failure")
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
// type safety; the conversion to the project's canonical int64 Unix-ms
// wire format happens at the proto boundaries (PostMessage's response and
// buildForwardMessage in events.go).
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
	}, passivationTimeout)
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
	slog.Info("grain.init",
		"grain_type", "RoomGrain",
		"grain_id", ctx.Identity(),
		"msg_type", "Init",
	)
}

// Terminate is a passivation hook; Phase 1 does not persist state.
func (g *Grain) Terminate(ctx cluster.GrainContext) {
	slog.Info("grain.terminate",
		"grain_type", "RoomGrain",
		"grain_id", ctx.Identity(),
		"msg_type", "Terminate",
	)
}

// ReceiveDefault logs unexpected messages without crashing the grain.
func (g *Grain) ReceiveDefault(ctx cluster.GrainContext) {
	slog.Warn("grain.unhandled",
		"grain_type", "RoomGrain",
		"grain_id", ctx.Identity(),
		"msg_type", fmt.Sprintf("%T", ctx.Message()),
	)
}

// Join adds the user to the room and fans out a JOINED event to every
// current member (including the joiner — multi-device echo, FR4).
func (g *Grain) Join(req *roompb.JoinRequest, ctx cluster.GrainContext) (*roompb.JoinResponse, error) {
	slog.Info("grain.msg",
		"grain_type", "RoomGrain",
		"grain_id", ctx.Identity(),
		"msg_type", "Join",
		"user_id", req.GetUserId(),
	)

	if req.GetUserId() == "" {
		return joinErr(codeInvalidRequest, statusInvalidRequest, "user_id is required"), nil
	}
	if g.state.isMember(req.GetUserId()) {
		return joinErr(codeRoomAlreadyMember, statusRoomAlreadyMember, "already a member of this room"), nil
	}

	g.state.addMember(req.GetUserId())
	recipients := g.state.memberIDs()
	g.fanOutNotify(ctx, recipients, buildJoinedEvent(ctx.Identity(), req.GetUserId()), "Join.fanout")

	return &roompb.JoinResponse{Success: true}, nil
}

// Leave removes the user from the room and fans out a LEFT event to the
// pre-removal member snapshot (including the leaver, so their connection
// can update UI state symmetrically with Join).
func (g *Grain) Leave(req *roompb.LeaveRequest, ctx cluster.GrainContext) (*roompb.LeaveResponse, error) {
	slog.Info("grain.msg",
		"grain_type", "RoomGrain",
		"grain_id", ctx.Identity(),
		"msg_type", "Leave",
		"user_id", req.GetUserId(),
	)

	if req.GetUserId() == "" {
		return leaveErr(codeInvalidRequest, statusInvalidRequest, "user_id is required"), nil
	}
	if !g.state.isMember(req.GetUserId()) {
		return leaveErr(codeRoomNotMember, statusRoomNotMember, "not a member of this room"), nil
	}

	// Snapshot before removal so the leaver also receives the LEFT event
	// (architecture decision documented in story 3.1 task 5).
	recipients := g.state.memberIDs()
	g.state.removeMember(req.GetUserId())
	g.fanOutNotify(ctx, recipients, buildLeftEvent(ctx.Identity(), req.GetUserId()), "Leave.fanout")

	return &roompb.LeaveResponse{Success: true}, nil
}

// PostMessage records the message, assigns the server-side timestamp, and
// fans the message out unconditionally to every current member (FR4).
func (g *Grain) PostMessage(req *roompb.PostMessageRequest, ctx cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	slog.Info("grain.msg",
		"grain_type", "RoomGrain",
		"grain_id", ctx.Identity(),
		"msg_type", "PostMessage",
		"user_id", req.GetUserId(),
	)

	if req.GetUserId() == "" {
		return postErr(codeInvalidRequest, statusInvalidRequest, "user_id is required"), nil
	}
	if strings.TrimSpace(req.GetText()) == "" {
		return postErr(codeMissingField, statusMissingField, "text is required"), nil
	}
	if !g.state.isMember(req.GetUserId()) {
		return postErr(codeRoomNotMember, statusRoomNotMember, "not a member of this room"), nil
	}

	timestamp := g.now()
	g.state.recordMessage(chatMessage{
		senderID:  req.GetUserId(),
		text:      req.GetText(),
		timestamp: timestamp,
	})

	recipients := g.state.memberIDs()
	payload := buildForwardMessage(ctx.Identity(), req.GetUserId(), req.GetText(), timestamp)
	g.fanOutForward(ctx, recipients, payload, "PostMessage.fanout")

	// Proto wire format is int64 Unix-ms; conversion at the response boundary.
	return &roompb.PostMessageResponse{Success: true, Timestamp: timestamp.UnixMilli()}, nil
}

// fanOutNotify delivers a NotifyRoomEvent to each recipient. Failures are
// logged at warn level but do not abort the operation — Phase 1 fan-out is
// best-effort (architecture.md "Process Patterns: no automatic retries").
//
// Phase 1 uses a sequential loop; concurrency can be added later if measured
// fan-out latency demands it.
func (g *Grain) fanOutNotify(ctx cluster.GrainContext, recipients []string, payload *userpb.NotifyRoomEventRequest, msgType string) {
	for _, recipientID := range recipients {
		if err := g.notifier.NotifyRoomEvent(recipientID, payload); err != nil {
			slog.Warn("grain.fanout.error",
				"grain_type", "RoomGrain",
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
func (g *Grain) fanOutForward(ctx cluster.GrainContext, recipients []string, payload *userpb.ForwardMessageRequest, msgType string) {
	for _, recipientID := range recipients {
		if err := g.notifier.ForwardMessage(recipientID, payload); err != nil {
			slog.Warn("grain.fanout.error",
				"grain_type", "RoomGrain",
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
		Success: false,
		Error:   &roompb.ErrorDetail{Code: code, Status: status, Message: msg},
	}
}

func leaveErr(code int32, status, msg string) *roompb.LeaveResponse {
	return &roompb.LeaveResponse{
		Success: false,
		Error:   &roompb.ErrorDetail{Code: code, Status: status, Message: msg},
	}
}

func postErr(code int32, status, msg string) *roompb.PostMessageResponse {
	return &roompb.PostMessageResponse{
		Success: false,
		Error:   &roompb.ErrorDetail{Code: code, Status: status, Message: msg},
	}
}
