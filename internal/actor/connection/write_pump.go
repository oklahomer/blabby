package connection

import (
	"context"
	"time"

	"github.com/gorilla/websocket"
)

const writeTimeout = 5 * time.Second

// runWritePump consumes outbound messages and writes them as WebSocket
// frames. It exits when ctx is cancelled (shutdown), when it processes a
// *CloseConnection (protocol-level close), or when a write fails. It does
// not close the WebSocket — the actor owns connection lifecycle.
//
// notify is the only way this loop talks back to its owner. The pump has
// no awareness of proto.actor; the caller wires notify to whatever
// delivery mechanism is appropriate (typically root.Send to the owning
// actor's PID).
func runWritePump(
	ctx context.Context,
	conn *websocket.Conn,
	outbound <-chan any,
	notify func(any),
) {
	for {
		select {
		case <-ctx.Done():
			// Shutdown-driven exit: the actor is already stopping, so no
			// *WritePumpClosed is needed.
			return

		case msg, ok := <-outbound:
			if !ok {
				// Defensive: in practice the actor's shutdown closes
				// outbound and cancels ctx together, so the ctx.Done()
				// arm normally fires first.
				return
			}

			if _, ok := msg.(*CloseConnection); ok {
				closeErr := conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(writeTimeout),
				)
				closed := &WritePumpClosed{}
				if closeErr != nil {
					closed.CloseFrameErr = "close_frame_write_error"
				}
				notify(closed)
				return
			}

			frame, ok := encodeOutboundMessage(msg)
			if !ok {
				continue
			}
			if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
				notify(&WritePumpFailed{Reason: "write_deadline_error", EventKind: frame.eventKind})
				return
			}
			if err := conn.WriteMessage(frame.messageType, frame.data); err != nil {
				notify(&WritePumpFailed{Reason: "write_error", EventKind: frame.eventKind})
				return
			}
		}
	}
}
