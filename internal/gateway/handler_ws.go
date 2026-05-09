package gateway

import (
	"log/slog"
	"net/http"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/internal/actor/connection"
)

// wsUpgrader is the package-private upgrader used by every /ws request.
//
// CheckOrigin currently accepts every origin. The Phase 1 client is a TUI
// that does not send Origin, so a permissive policy is correct today.
// Before any browser client ships, the policy must be tightened to a
// configurable allow-list.
//
// TODO(security): replace with a configurable origin allow-list before
// any browser client connects.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1 << 12,
	WriteBufferSize: 1 << 12,
	CheckOrigin:     func(_ *http.Request) bool { return true },
}

// handleWS upgrades the HTTP request to a WebSocket connection and spawns
// a UserConnection actor to manage the session. Auth happens on the
// upgraded socket as the first text frame, not via the Authorization
// header — see ADR-004.
//
// If the gateway is missing its cluster or actor-root dependencies, the
// handler responds 503 + 5002 with a JSON envelope BEFORE attempting the
// upgrade. After a failed upgrade gorilla writes its own response, so we
// must not call WriteErrorResponse on that path.
func (g *Gateway) handleWS(w http.ResponseWriter, r *http.Request) {
	if g.auth == nil || g.cluster == nil || g.actorRoot == nil {
		WriteErrorResponse(w, http.StatusServiceUnavailable,
			ErrServiceUnavailable("websocket endpoint not available"))
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws.upgrade.failed",
			"method", r.Method,
			"path", r.URL.Path,
			"remote_addr", r.RemoteAddr,
			"reason", "upgrade_failed",
		)
		// gorilla has already written an HTTP response on the failure
		// path; do not write another.
		return
	}

	// If Spawn panics or otherwise returns without producing a PID
	// (Props misconfiguration, actor system shutdown, etc.) the upgraded
	// conn would otherwise leak its fd. Defer closes it defensively when
	// the spawn never produced a PID; the actor owns the conn from then on.
	var pid *actor.PID
	defer func() {
		if pid == nil {
			_ = conn.Close()
		}
	}()

	props := connection.NewProps(conn, g.auth, g.cluster)
	pid = g.actorRoot.Spawn(props)
	slog.Info("ws.upgrade.ok",
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
		"pid", pid.String(),
	)
}
