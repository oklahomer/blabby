// Package graintest provides minimal helpers for unit-testing grain message
// handlers without standing up a full proto.actor cluster.
//
// The helpers in this package are deliberately small — they grow on
// demand as new grain tests need richer test seams.
//
// These helpers are for in-process unit tests only; do not import them from
// production wiring.
package graintest

import (
	"sync"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
)

// fakeGrainContext is a no-cluster stand-in for [cluster.GrainContext].
//
// It satisfies the interface at compile time by embedding [actor.Context] as
// a nil field. Only Identity, Kind, Cluster, Message, and Sender are
// implemented; calling any other embedded actor.Context method will panic.
// That is intentional — tests should never reach the actor runtime through
// the fake.
type fakeGrainContext struct {
	actor.Context
	identity string
	kind     string
	message  any
	sender   *actor.PID
	watcher  *WatchRecorder
}

func (f *fakeGrainContext) Identity() string          { return f.identity }
func (f *fakeGrainContext) Kind() string              { return f.kind }
func (f *fakeGrainContext) Cluster() *cluster.Cluster { return nil }
func (f *fakeGrainContext) Message() any              { return f.message }
func (f *fakeGrainContext) Sender() *actor.PID        { return f.sender }

// Watch records the PID when a recorder is attached; otherwise it is a
// no-op. Overriding Watch keeps the embedded actor.Context.Watch (which
// would panic on the nil embed) out of reach for tests that exercise grain
// handlers calling ctx.Watch.
func (f *fakeGrainContext) Watch(pid *actor.PID) {
	if f.watcher != nil {
		f.watcher.record(pid)
	}
}

// Unwatch is a no-op symmetric with Watch — provided so a future grain
// handler that calls ctx.Unwatch under test does not panic on the nil
// embed. Tests that need to assert on Unwatch can extend WatchRecorder.
func (f *fakeGrainContext) Unwatch(*actor.PID) {}

// FakeGrainContextOption configures an optional field on the fake context.
// Used by NewFakeGrainContext for opt-in features (sender PID, kind override,
// inbound message) so call sites stay short for the common case.
type FakeGrainContextOption func(*fakeGrainContext)

// WithKind overrides the default kind reported by the fake context.
func WithKind(kind string) FakeGrainContextOption {
	return func(f *fakeGrainContext) { f.kind = kind }
}

// WithSender installs a sender PID that the fake context returns from
// Sender(). The default (no option) returns nil — callers exercising the
// "nil sender" defensive path can rely on that.
func WithSender(pid *actor.PID) FakeGrainContextOption {
	return func(f *fakeGrainContext) { f.sender = pid }
}

// WithMessage installs the value returned by Message(). Useful for
// exercising ReceiveDefault.
func WithMessage(msg any) FakeGrainContextOption {
	return func(f *fakeGrainContext) { f.message = msg }
}

// WithWatchRecorder installs a recorder that captures every Watch call
// made through the returned context. Tests use it to assert that a
// handler armed protoactor's death-watch on the expected PID.
func WithWatchRecorder(w *WatchRecorder) FakeGrainContextOption {
	return func(f *fakeGrainContext) { f.watcher = w }
}

// WatchRecorder collects PIDs passed to ctx.Watch on a fakeGrainContext.
// The zero value is ready to use; PIDs() returns a snapshot in call order.
// Safe for concurrent use so a future test that drives the grain from a
// real actor goroutine doesn't race the test goroutine reading PIDs().
type WatchRecorder struct {
	mu   sync.Mutex
	pids []*actor.PID
}

func (w *WatchRecorder) record(pid *actor.PID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pids = append(w.pids, pid)
}

// PIDs returns the PIDs Watch was called with, in call order.
func (w *WatchRecorder) PIDs() []*actor.PID {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]*actor.PID, len(w.pids))
	copy(out, w.pids)
	return out
}

// defaultKind is the value returned by Kind() when no explicit kind option
// is supplied. The empty string is intentional: most tests do not consult
// Kind() (only ReceiveDefault-shaped diagnostics tend to), so a default of
// "" makes any accidental dependency on a hardcoded grain name fail loudly
// rather than read a misleading value (the prior default "RoomGrain"
// silently lied to User grain tests).
const defaultKind = ""

// NewFakeGrainContext returns a [cluster.GrainContext] that reports the given
// identity. The kind defaults to the empty string; pass [WithKind] when a
// test inspects Kind().
//
// It does NOT support sending or receiving messages, spawning children, or
// any other actor.Context operation not listed above; calling those will
// panic. Use a real cluster (see internal/testutil/cluster) when you need
// integration coverage.
func NewFakeGrainContext(identity string, opts ...FakeGrainContextOption) cluster.GrainContext {
	f := &fakeGrainContext{identity: identity, kind: defaultKind}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// NewFakeGrainContextWithMessage is a thin convenience wrapper kept for
// existing call sites; equivalent to NewFakeGrainContext with WithMessage.
func NewFakeGrainContextWithMessage(identity string, message any) cluster.GrainContext {
	return NewFakeGrainContext(identity, WithMessage(message))
}

// NewJoinRequest returns a roompb.JoinRequest with the given user_id, kept
// short so test bodies can stay tabular.
func NewJoinRequest(userID string) *roompb.JoinRequest {
	return &roompb.JoinRequest{UserId: userID}
}

// NewLeaveRequest returns a roompb.LeaveRequest with the given user_id.
func NewLeaveRequest(userID string) *roompb.LeaveRequest {
	return &roompb.LeaveRequest{UserId: userID}
}

// NewPostMessageRequest returns a roompb.PostMessageRequest with the given
// sender user_id and message text.
func NewPostMessageRequest(userID, text string) *roompb.PostMessageRequest {
	return &roompb.PostMessageRequest{UserId: userID, Text: text}
}
