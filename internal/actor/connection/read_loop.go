package connection

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/gorilla/websocket"
)

const maxInboundFrameBytes = 64 << 10

// runReadPump reads frames from conn, decodes supported text frames into
// typed messages, and forwards those messages via notify. On a wire-level
// error it emits exactly one *ConnectionClosed and returns. When ctx is
// cancelled (signalled by the actor's shutdown path), the loop returns
// silently without emitting a *ConnectionClosed. Start it from the actor's
// *actor.Started handler exactly once per connection.
//
// notify is the only way this loop talks back to its owner. The pump has
// no awareness of proto.actor; the caller wires notify to whatever
// delivery mechanism is appropriate (typically root.Send to the owning
// actor's PID).
//
// runReadPump never closes the WebSocket. The actor owns connection
// lifecycle; conn.Close from the actor's shutdown is what unblocks an
// in-flight ReadMessage so this loop can iterate back to the select and
// observe ctx.Done().
func runReadPump(ctx context.Context, conn *websocket.Conn, notify func(any)) {
	defer func() {
		if r := recover(); r != nil {
			notify(&ReadPumpPanicked{Cause: fmt.Sprint(r)})
		}
	}()

	conn.SetReadLimit(maxInboundFrameBytes)

	for {
		select {
		case <-ctx.Done():
			return

		default:
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				// Differentiate shutdown-driven unblocks (the actor's
				// shutdown ran → ctx already cancelled) from real wire-level
				// errors. The next iteration's select would also catch
				// ctx.Done(), but checking here avoids emitting a redundant
				// *ConnectionClosed during shutdown.
				if ctx.Err() != nil {
					return
				}
				notify(&ConnectionClosed{Reason: classifyReadErr(err)})
				return
			}
			if msgType != websocket.TextMessage {
				// Binary frames are outside the current application protocol.
				continue
			}
			notify(decodeInboundFrame(data))
		}
	}
}

// classifyReadErr returns a coarse, log-safe label for a read error. The
// underlying error string is intentionally NOT propagated to avoid leaking
// transport details.
func classifyReadErr(err error) string {
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
	) {
		return "closed_by_peer"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "read_timeout"
	}
	return "read_error"
}
