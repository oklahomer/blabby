// Package graintest provides minimal helpers for unit-testing grain message
// handlers without standing up a full proto.actor cluster.
//
// The helpers in this package are deliberately small — Story 3.1 only needs a
// fake [cluster.GrainContext] that reports an identity, and constructors for
// the proto request types Room grain handles. Stories 3.2 and 3.3 will extend
// the helpers as new grains require richer test seams.
//
// These helpers are for in-process unit tests only; do not import them from
// production wiring.
package graintest

import (
	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	roompb "github.com/oklahomer/blabby/gen/room"
)

// fakeGrainContext is a no-cluster stand-in for [cluster.GrainContext].
//
// It satisfies the interface at compile time by embedding [actor.Context] as
// a nil field. Only Identity, Kind, and Cluster are implemented; calling any
// other embedded actor.Context method will panic. That is intentional — tests
// should never reach the actor runtime through the fake.
type fakeGrainContext struct {
	actor.Context
	identity string
	kind     string
	message  any
}

func (f *fakeGrainContext) Identity() string          { return f.identity }
func (f *fakeGrainContext) Kind() string              { return f.kind }
func (f *fakeGrainContext) Cluster() *cluster.Cluster { return nil }
func (f *fakeGrainContext) Message() any              { return f.message }

// NewFakeGrainContext returns a [cluster.GrainContext] that reports the given
// identity and a fixed kind of "RoomGrain" for tests.
//
// It does NOT support sending or receiving messages, spawning children, or
// any other actor.Context operation; calling those will panic. Use a real
// cluster (see internal/testutil/cluster) when you need integration coverage.
func NewFakeGrainContext(identity string) cluster.GrainContext {
	return &fakeGrainContext{identity: identity, kind: "RoomGrain"}
}

// NewFakeGrainContextWithMessage is like NewFakeGrainContext but also returns
// the supplied value from Message(). Useful for exercising ReceiveDefault.
func NewFakeGrainContextWithMessage(identity string, message any) cluster.GrainContext {
	return &fakeGrainContext{identity: identity, kind: "RoomGrain", message: message}
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
