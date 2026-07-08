// Package room implements the Room grain — a virtual actor that owns chat
// room membership and the canonical post-message → fan-out pipeline.
//
// Per the project's naming convention, the externally visible type is
// [room.Grain]; do NOT rename it to RoomGrain (that would stutter with the
// package name and trip golangci-lint's revive rule).
//
// Room metadata, membership, and timeline writes are DB-authoritative. Each
// activation hydrates the room/member refs it needs from persistence and keeps
// an in-memory working cache for command handling and best-effort fan-out.
package room

import (
	"context"
	"errors"
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
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/middleware"
	"github.com/oklahomer/blabby/internal/persistence"
)

// Event-name constants for every log line this package emits. Follow the
// N3 past-tense action-verb convention: <package>.<object>.<past-verb> for
// happy paths, <package>.<object>.<base-verb>_rejected for validation
// refusals. Cross-cutting events (grain.fanout, grain.fanout.error,
// grain.unhandled) live in internal/middleware as exported constants and
// are referenced via middleware.EventXxx.
const (
	eventRoomMemberJoined              = "room.member.joined"
	eventRoomMemberJoinRejected        = "room.member.join_rejected"
	eventRoomMemberLeft                = "room.member.left"
	eventRoomMemberLeaveRejected       = "room.member.leave_rejected"
	eventRoomRoleChanged               = "room.role.changed"
	eventRoomRoleChangeRejected        = "room.role.change_rejected"
	eventRoomOwnershipTransferred      = "room.ownership.transferred"
	eventRoomOwnershipTransferRejected = "room.ownership.transfer_rejected"
	eventRoomMessagePosted             = "room.message.posted"
	eventRoomMessagePostRejected       = "room.message.post_rejected"
	eventRoomMessageWriteFailed        = "room.message.write_failed"
	eventRoomFanoutSupervision         = "room.fanout.supervision"
	eventRoomActivationRejected        = "room.activation.rejected"
	eventRoomMembershipWriteFailed     = "room.membership.write_failed"
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
	state      roomState
	loader     RoomLoader
	membership MembershipStore
	messages   MessageStore
	notifier   userNotifier
	now        func() time.Time
	fanout     fanoutDispatcher
}

// roomGrainKind is the cluster kind name registered by the generated
// NewRoomGrainKind. The log envelopes below must agree with it.
const roomGrainKind = "RoomGrain"

// Option configures a Room kind built by NewKind.
type Option func(*kindConfig)

// kindConfig collects the per-kind collaborators captured into each activation's
// Grain by NewKind's producer.
type kindConfig struct {
	loader     RoomLoader
	membership MembershipStore
	messages   MessageStore
}

// WithMembership injects the DB-authoritative membership store. Production wires
// it (via clusterboot); a grain built without it keeps membership in memory only
// (no durability), which is the behavior unit tests rely on unless they opt in.
func WithMembership(store MembershipStore) Option {
	return func(c *kindConfig) { c.membership = store }
}

// WithMessages injects the durable message store. Production wires it (via
// clusterboot); a grain built without it keeps messages in memory only (no
// durability), mirroring WithMembership's convention for unit tests.
func WithMessages(store MessageStore) Option {
	return func(c *kindConfig) { c.messages = store }
}

// NewKind registers Room grain with a proto.actor cluster, returning a
// *cluster.Kind that callers pass to cluster.WithKinds(...). The loader hydrates
// each activation's RoomRef from the source of truth; it is injected here so the
// production path reaches persistence while tests inject a stub. The default
// userNotifier is built lazily during Init from ctx.Cluster(), avoiding a
// chicken-and-egg with cluster.New requiring kinds in its config.
func NewKind(loader RoomLoader, opts ...Option) *cluster.Kind {
	if loader == nil {
		// Fail at wiring time with a clear message rather than as an opaque nil
		// interface panic inside the first activation's hydrate.
		panic("room: nil RoomLoader")
	}
	cfg := kindConfig{loader: loader}
	for _, opt := range opts {
		opt(&cfg)
	}
	return roompb.NewRoomGrainKind(func() roompb.RoomGrain {
		return &Grain{loader: cfg.loader, membership: cfg.membership, messages: cfg.messages}
	}, passivationTimeout,
		actor.WithReceiverMiddleware(middleware.GrainLogging(roomGrainKind)),
		// Govern the fan-out worker child: protoactor resolves a child's
		// supervisor from its parent's props. Unexpected worker panics restart
		// the stateless actor instance while preserving its PID and remaining
		// mailbox; see supervision.go.
		actor.WithSupervisor(newFanoutSupervisor(nil)),
	)
}

// Init hydrates a freshly activated Room grain from the source of truth, then
// wires the command-path collaborators — but only for a valid (active) room.
//
// Hydration runs first because its outcome decides whether the grain is usable:
//   - active room → cache the RoomRef and set up the notifier and fan-out child;
//     the grain serves commands.
//   - absent or non-active room → leave the grain unloaded and return early; no
//     collaborators are created because every command short-circuits to
//     ROOM_NOT_FOUND, and an invalid grain never fans out.
//   - any other (transient) load error → panic before any child is spawned, so
//     the activation fails cleanly and the supervisor re-activates the grain on
//     a later request rather than serving a half-initialized room.
//
// When the grain was constructed without an injected notifier (the production
// path), a clusterUserNotifier is built from ctx.Cluster() so fan-out reaches
// the real User grain.
func (g *Grain) Init(ctx cluster.GrainContext) {
	g.state = newRoomState()
	if !g.hydrate(ctx) {
		return
	}
	// Seed the member cache from the source of truth before any collaborators
	// are created, so a transient load failure panics the activation (re-tried
	// by the supervisor) rather than serving a room with a partial member set.
	g.loadMembers()

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

// hydrate loads the room's reference metadata for this activation and reports
// whether the grain is now usable. The grain identity is the RoomID's decimal
// form, minted by the cluster; a malformed one is a programmer error upstream,
// so it leaves the grain unloaded rather than crashing the cluster. A transient
// load failure panics (see Init for how each outcome maps to grain state).
func (g *Grain) hydrate(ctx cluster.GrainContext) bool {
	roomID, err := id.ParseRoomID(ctx.Identity())
	if err != nil {
		slog.Error(eventRoomActivationRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", "malformed_identity",
			"error", err,
		)
		return false
	}

	ref, err := g.loader.LoadRoom(context.Background(), roomID)
	switch {
	case err == nil && ref.Status() == domain.RoomStatusActive:
		g.state.loadRoom(ref)
		return true
	case errors.Is(err, ErrRoomNotFound):
		slog.Warn(eventRoomActivationRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", errcode.RoomNotFound.Status(),
		)
		return false
	case err == nil:
		// A room that exists but is not active (e.g. archived) is addressable by
		// id but not joinable: leave the grain unloaded so commands are rejected.
		slog.Warn(eventRoomActivationRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", errcode.RoomNotFound.Status(),
			"status", string(ref.Status()),
		)
		return false
	default:
		// Transient failure (e.g. database unreachable). Crash the activation so
		// the supervisor re-activates the grain on a later request rather than
		// serving a half-initialized room.
		panic(fmt.Errorf("room: hydrate %s: %w", roomID, err))
	}
}

// loadMembers seeds the in-memory member cache from DB-authoritative membership
// on activation, so it survives passivation / node loss. A transient load
// failure panics (like hydrate) so the supervisor re-activates rather than
// serving a room with an incomplete roster. With no membership store wired
// (tests exercising only in-memory behavior) it is a no-op.
func (g *Grain) loadMembers() {
	if g.membership == nil {
		return
	}
	roomID := g.state.roomRef().ID()
	members, err := g.membership.LoadMembers(context.Background(), roomID)
	if err != nil {
		panic(fmt.Errorf("room: load members %s: %w", roomID, err))
	}
	for _, ref := range members {
		g.state.addMember(ref)
	}
}

// Terminate is a passivation hook; membership is DB-authoritative and reloaded
// on the next activation, so there is nothing to flush here.
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
	if !g.state.isLoaded() {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", errcode.RoomNotFound.Status(),
		)
		return joinErr(errcode.RoomNotFound, "room not found"), nil
	}
	// The grain is loaded, so every response below carries the room's RoomRef
	// (the joiner caches it) and fan-out embeds the same ref. Only the
	// ROOM_NOT_FOUND guard above omits it.
	roomRef := g.state.roomRef()
	room := protoRoomRef(roomRef)

	joiner, err := parseUserRef(req.GetUser())
	if err != nil {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", req.GetUser().GetId(),
			"reason", errcode.InvalidRequest.Status(),
			"error", err,
		)
		return joinErrWithRoom(errcode.InvalidRequest, "user id and display name are required", room), nil
	}
	userID := joiner.ID()
	if g.state.isMember(userID) {
		slog.Warn(eventRoomMemberJoinRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", errcode.RoomAlreadyMember.Status(),
		)
		return joinErrWithRoom(errcode.RoomAlreadyMember, "already a member of this room", room), nil
	}

	// Persist the transition (membership row + member_joined event, one txn)
	// before touching in-memory state or fanning out, so a durable-write failure
	// leaves no cache/timeline drift (fail-closed).
	evt, err := g.recordJoin(ctx, joiner)
	if err != nil {
		return joinErrWithRoom(errcode.InternalError, "failed to record membership", room), nil
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
	g.fanOutNotify(ctx, recipients, buildJoinedEvent(roomRef, joiner, evt), "Join.fanout")

	return &roompb.JoinResponse{Room: room}, nil
}

// Leave removes the user from the room and fans out a LEFT event to the
// pre-removal member snapshot (including the leaver, so their connection
// can update UI state symmetrically with Join).
func (g *Grain) Leave(req *roompb.LeaveRequest, ctx cluster.GrainContext) (*roompb.LeaveResponse, error) {
	if !g.state.isLoaded() {
		slog.Warn(eventRoomMemberLeaveRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", errcode.RoomNotFound.Status(),
		)
		return leaveErr(errcode.RoomNotFound, "room not found"), nil
	}
	userID, err := id.ParseUserID(req.GetUserId())
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

	// Persist the transition (row delete + member_left event, one txn) before
	// mutating in-memory state or fanning out (fail-closed).
	evt, err := g.recordLeave(ctx, leaver)
	if errors.Is(err, persistence.ErrOwnerCannotLeave) {
		slog.Warn(eventRoomMemberLeaveRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", userID,
			"reason", errcode.RoomOwnerCannotLeave.Status(),
		)
		return leaveErr(errcode.RoomOwnerCannotLeave, "transfer ownership before leaving the room"), nil
	}
	if err != nil {
		return leaveErr(errcode.InternalError, "failed to record membership"), nil
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
	g.fanOutNotify(ctx, recipients, buildLeftEvent(g.state.roomRef(), leaver, evt), "Leave.fanout")

	return &roompb.LeaveResponse{}, nil
}

// recordMessage durably appends the message_posted event and returns its
// identity, whose occurred_at becomes the message's timestamp on the response
// and every fan-out copy. With no message store wired it is a no-op returning a
// zero event; production always wires one.
func (g *Grain) recordMessage(ctx cluster.GrainContext, author id.UserID, text string) (MessageEvent, error) {
	if g.messages == nil {
		return MessageEvent{}, nil
	}
	evt, err := g.messages.RecordMessage(context.Background(), g.state.roomRef().ID(), author, text)
	if err != nil {
		slog.Error(eventRoomMessageWriteFailed,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", author,
			"error", err,
		)
		return MessageEvent{}, err
	}
	return evt, nil
}

// recordJoin durably persists a join (membership row + member_joined event in one
// transaction) and returns the event identity for fan-out. With no membership
// store wired it is a no-op returning a zero event; production always wires one.
func (g *Grain) recordJoin(ctx cluster.GrainContext, joiner domain.UserRef) (MembershipEvent, error) {
	if g.membership == nil {
		return MembershipEvent{}, nil
	}
	evt, err := g.membership.RecordJoin(context.Background(), g.state.roomRef().ID(), joiner)
	if err != nil {
		slog.Error(eventRoomMembershipWriteFailed,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", joiner.ID(),
			"transition", "join",
			"error", err,
		)
		return MembershipEvent{}, err
	}
	return evt, nil
}

// recordLeave is recordJoin's mirror: it persists the row delete and member_left
// event in one transaction. An owner-refused leave is an expected business
// outcome the caller maps to its own rejection; only real write failures are
// logged here.
func (g *Grain) recordLeave(ctx cluster.GrainContext, leaver domain.UserRef) (MembershipEvent, error) {
	if g.membership == nil {
		return MembershipEvent{}, nil
	}
	evt, err := g.membership.RecordLeave(context.Background(), g.state.roomRef().ID(), leaver)
	if err != nil && errors.Is(err, persistence.ErrOwnerCannotLeave) {
		return MembershipEvent{}, err
	}
	if err != nil {
		slog.Error(eventRoomMembershipWriteFailed,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"user_id", leaver.ID(),
			"transition", "leave",
			"error", err,
		)
		return MembershipEvent{}, err
	}
	return evt, nil
}

// PostMessage records the message, assigns the server-side timestamp, and
// fans the message out unconditionally to every current member.
func (g *Grain) PostMessage(req *roompb.PostMessageRequest, ctx cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	if !g.state.isLoaded() {
		slog.Warn(eventRoomMessagePostRejected,
			"grain_type", ctx.Kind(),
			"grain_id", ctx.Identity(),
			"reason", errcode.RoomNotFound.Status(),
		)
		return postErr(errcode.RoomNotFound, "room not found"), nil
	}
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

	// Fail-closed: every state change — the roster's display-name refresh, the
	// recent-message cache, the fan-out — happens only once the message is
	// durable. Without a store (memory-only unit tests) the grain clock stands
	// in for the DB's occurred_at.
	evt, err := g.recordMessage(ctx, userID, req.GetText())
	if err != nil {
		return postErr(errcode.InternalError, "failed to record message"), nil
	}
	timestamp := evt.OccurredAt
	eventID := ""
	if evt.IsZero() {
		timestamp = g.now()
	} else {
		eventID = evt.ID.String()
	}
	// Refresh the cached name from the value carried on this message, so the
	// room's roster reflects the sender's current display name.
	g.state.refreshMember(sender)
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
	payload := buildForwardMessage(g.state.roomRef(), sender, req.GetText(), timestamp, eventID)
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

// parseUserRef parses an inbound proto UserRef into a validated domain.UserRef (id +
// public code + display name) at the grain boundary (parse, don't validate). A
// nil ref, an invalid id, a missing/invalid public code, or an empty name is
// rejected so handlers can return INVALID_REQUEST. Requiring the public code
// here means a sender/actor whose public identity could not be resolved fails
// closed rather than fanning its internal id out to clients.
func parseUserRef(p *commonpb.UserRef) (domain.UserRef, error) {
	userID, err := id.ParseUserID(p.GetId())
	if err != nil {
		return domain.UserRef{}, err
	}
	code, err := id.ParsePublicCode(p.GetPublicCode())
	if err != nil {
		return domain.UserRef{}, fmt.Errorf("user_ref: public_code: %w", err)
	}
	return domain.NewUserRef(userID, code, p.GetName())
}

func joinErr(code errcode.Code, msg string) *roompb.JoinResponse {
	return &roompb.JoinResponse{
		Error: &commonpb.ErrorDetail{Code: code.Int32(), Status: code.Status(), Message: msg},
	}
}

// joinErrWithRoom is joinErr plus the room's RoomRef, for rejections issued once
// the grain is loaded (so the caller can still cache the room metadata).
func joinErrWithRoom(code errcode.Code, msg string, room *commonpb.RoomRef) *roompb.JoinResponse {
	resp := joinErr(code, msg)
	resp.Room = room
	return resp
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
