package connection

import (
	"log/slog"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/supervision"
)

// connectionSupervisor is shared by every production UserConnection spawn.
// protoactor caches guardian processes by strategy instance, so reusing this
// value avoids creating one guardian process per WebSocket.
var connectionSupervisor = newConnectionSupervisor(nil)

// classifyConnectionFailure is the UserConnection supervision decider. It is
// pure — no logging, no side effects — so a test can assert the directive for
// each cause, and the paired Proto.Actor strategy can apply the directive.
//
// Every cause maps to Stop, so the decider needs no cause inspection:
//
//   - Restart is wrong because the actor's identity is the underlying
//     WebSocket; a fresh instance would inherit a dead socket with nothing to
//     serve. The client's reconnect — new socket, new actor — is the real
//     restart.
//   - Resume is wrong because the session is an ordered protocol stream;
//     skipping one message would silently corrupt it for the peer.
//   - Escalate is impossible: UserConnection is root-spawned under a
//     protoactor guardian, and guardians panic on EscalateFailure.
//
// This covers the one modeled cause — *outboundBackpressureError, whose doc
// carries the policy rationale — and any unanticipated panic alike. The
// supervisor only sees panics raised on the actor's message-handling path:
// panics inside the read/write pump goroutines are recovered locally by those
// goroutines and reported back as *ReadPumpPanicked / *WritePumpFailed
// messages, which Receive's transport tear-down section handles directly.
func classifyConnectionFailure(_ any) actor.Directive {
	return actor.StopDirective
}

// connectionAttrs builds the supervision log envelope for a UserConnection,
// mirroring the actor_type / actor_path fields ActorLogging emits so the
// supervision line joins the rest of the connection's log trail.
func connectionAttrs(child *actor.PID, _ any) []slog.Attr {
	return []slog.Attr{
		slog.String("actor_type", actorType),
		slog.String("actor_path", child.String()),
	}
}

// newConnectionSupervisor builds the guardian strategy for a root-spawned
// UserConnection. A nil logger falls back to slog.Default(). Exposed as a
// factory so a test can wire it with a capture logger and assert the
// supervision log envelope.
func newConnectionSupervisor(logger *slog.Logger) actor.SupervisorStrategy {
	strategy, err := supervision.NewLoggingStrategy(supervision.Config{
		Event:   eventConnectionSupervision,
		Decider: classifyConnectionFailure,
		Attrs:   connectionAttrs,
		Logger:  logger,
		// The zero restart budget is irrelevant here: the decider never
		// returns Restart, so the one-for-one strategy only ever stops.
		Apply: actor.NewOneForOneStrategy(0, 0, classifyConnectionFailure),
	})
	if err != nil {
		panic(err)
	}
	return strategy
}

// outboundBackpressureError is panicked by sendOutbound when the outbound
// channel is full. classifyConnectionFailure maps it to actor.StopDirective so
// the actor tears down via the normal Stopping path rather than dropping
// messages silently.
//
// Backpressure here means the write pump cannot keep up with the producer,
// which in our protocol indicates a stuck or hostile peer. Three policies were
// considered:
//   - Block the producer — would freeze the actor on any slow client.
//   - Drop and continue — would corrupt chat ordering and the auth handshake.
//   - Tear down the connection — chosen; the client reconnects fresh.
//
// Restart is meaningless here because the WebSocket lives in this actor
// instance, so Stop is the only viable directive.
type outboundBackpressureError struct{}

func (e *outboundBackpressureError) Error() string { return "outbound channel backpressure" }
