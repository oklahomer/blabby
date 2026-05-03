package user

import (
	"github.com/asynkron/protoactor-go/actor"
	"google.golang.org/protobuf/proto"
)

// pidSender abstracts ctx.Send to a single connection PID for testability.
//
// In production this is a closure over the active GrainContext.Send; in
// tests it is a recording function. We use a function type rather than an
// interface because the production implementation is a one-line forward —
// inventing an interface to satisfy the test would be an abstraction beyond
// what the task requires.
type pidSender func(pid *actor.PID, msg proto.Message)
