package room

import (
	"log/slog"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/supervision"
)

// classifyFanoutFailure restarts the stateless fan-out worker after any
// unexpected panic. The failed job is dropped by Proto.Actor; the actor is
// rebuilt under the same PID and continues with the remaining mailbox.
// Expected per-recipient delivery failures stay on the ordinary error path in
// runNotify and runForward, where they are logged and skipped without crashing
// the worker.
//
// The restart budget is deliberately unlimited: a capped strategy would stop
// the long-lived worker after repeated failures, leaving the grain's
// dispatcher holding a dead PID and silently discarding every later fan-out
// job for the rest of the activation. Unlimited restarts keep fan-out alive
// for healthy jobs and lose only the panicking ones.
func classifyFanoutFailure(_ any) actor.Directive {
	return actor.RestartDirective
}

// fanoutAttrs builds the supervision log envelope from the fan-out job the
// worker panicked on, so grain_type and grain_id travel with the supervision
// line. An unrecognized message falls back to the worker PID.
func fanoutAttrs(child *actor.PID, message any) []slog.Attr {
	switch m := message.(type) {
	case *fanoutNotify:
		return []slog.Attr{
			slog.String("grain_type", m.grainKind),
			slog.String("grain_id", m.grainID),
		}
	case *fanoutForward:
		return []slog.Attr{
			slog.String("grain_type", m.grainKind),
			slog.String("grain_id", m.grainID),
		}
	default:
		return []slog.Attr{
			slog.String("grain_type", roomGrainKind),
			slog.String("actor_path", child.String()),
		}
	}
}

// newFanoutSupervisor builds the strategy that governs the Room grain's
// fan-out worker child. A nil logger falls back to slog.Default().
// actor.NewRestartingStrategy is Proto.Actor's native always-restart policy —
// the same mapping classifyFanoutFailure describes, applied with the
// runtime's own restart and event machinery.
func newFanoutSupervisor(logger *slog.Logger) actor.SupervisorStrategy {
	strategy, err := supervision.NewLoggingStrategy(supervision.Config{
		Event:   eventRoomFanoutSupervision,
		Decider: classifyFanoutFailure,
		Attrs:   fanoutAttrs,
		Logger:  logger,
		Apply:   actor.NewRestartingStrategy(),
	})
	if err != nil {
		panic(err)
	}
	return strategy
}
