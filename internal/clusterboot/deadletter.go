package clusterboot

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"github.com/asynkron/protoactor-go/eventstream"
)

const (
	// deadLetterLogThrottleCount and deadLetterLogThrottleInterval mirror
	// proto.actor's built-in dead-letter log throttle defaults, applying the
	// same valve mechanics as the built-in Info line that the Warn logger gate
	// drops. Those mechanics log fewer lines than a plain reading of the count
	// suggests: the valve leaves Open on the count-th event, so count 3 logs at
	// most two observed lines per window (see newDeadLetterLogHandler).
	deadLetterLogThrottleCount    int32 = 3
	deadLetterLogThrottleInterval       = time.Second
)

// SubscribeDeadLetterLogging subscribes a throttled, content-free dead-letter
// log to c's EventStream and returns the subscription. Subscribe before
// StartMember or StartClient and Unsubscribe on shutdown, or let it die with the
// actor system.
//
// The actor system publishes a *actor.DeadLetterEvent for every message sent to
// a nonexistent PID — a stale connection PID after a socket dies, or a fan-out
// racing a grain passivation or a member departure. The handler logs only the
// target PID, the message's Go type name, and the sender PID when present; it
// never logs the message body, which may carry credentials or user text.
//
// Dead letters are logged at Warn: they are anomalies worth an operator's
// attention but not necessarily errors. Some are benign shutdown races — a Stop
// or Watch reaching an actor that has just terminated — which is why the level
// is Warn rather than Error. Messages whose type implements
// actor.IgnoreDeadLetterLogging are skipped, matching the built-in subscriber's
// contract.
func SubscribeDeadLetterLogging(c *cluster.Cluster) *eventstream.Subscription {
	handle := newDeadLetterLogHandler(deadLetterLogThrottleCount, deadLetterLogThrottleInterval)
	return c.ActorSystem.EventStream.Subscribe(handle)
}

// newDeadLetterLogHandler builds the EventStream subscription handler. The
// throttle parameters are injected so tests can drive it with a tiny window.
//
// The throttle is proto.actor's own actor.NewThrottle, and the handler logs
// only while the valve is Open. The valve returns Closing on the count-th
// event of a window and Closed beyond it, so of a burst the first count-1
// events log an observed line and the rest are dropped. When the window ends,
// the callback reports how many events exceeded count; the count-th (Closing)
// event is dropped but not included in that number. The callback runs on a
// timer goroutine, so its throttled line may lag the burst. These are the same
// mechanics the built-in subscriber applies to its own line.
func newDeadLetterLogHandler(count int32, interval time.Duration) func(evt any) {
	throttle := actor.NewThrottle(count, interval, func(dropped int32) {
		slog.Warn("server.deadletter.throttled", "dropped", dropped)
	})
	return func(evt any) {
		dl, ok := evt.(*actor.DeadLetterEvent)
		if !ok {
			return
		}
		if _, ignore := dl.Message.(actor.IgnoreDeadLetterLogging); ignore {
			return
		}
		if throttle() != actor.Open {
			return
		}
		attrs := []any{"pid", pidString(dl.PID), "msg_type", deadLetterTypeName(dl.Message)}
		if dl.Sender != nil {
			attrs = append(attrs, "sender", dl.Sender.String())
		}
		slog.Warn("server.deadletter.observed", attrs...)
	}
}

// pidString renders a PID for logging, tolerating a nil PID.
func pidString(pid *actor.PID) string {
	if pid == nil {
		return ""
	}
	return pid.String()
}

// deadLetterTypeName returns the Go type name of msg with any leading '*'
// trimmed, so "*pkg.T" logs as "pkg.T". It never inspects message fields, so a
// message body cannot leak through the type name.
func deadLetterTypeName(msg any) string {
	return strings.TrimPrefix(fmt.Sprintf("%T", msg), "*")
}
