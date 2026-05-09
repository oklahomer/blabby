package gateway

import (
	"net/http"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	"github.com/oklahomer/blabby/internal/auth"
)

// Gateway is the HTTP entry point for the chat service. It translates
// between client-facing JSON / WebSocket frames and internal services.
//
// cluster and actorRoot are required only for the WebSocket endpoint;
// callers that exercise only HTTP routes may pass nil for both. /ws will
// respond 503 + 5002 (SERVICE_UNAVAILABLE) when either is missing.
type Gateway struct {
	auth      auth.Authenticator
	cluster   *cluster.Cluster
	actorRoot *actor.RootContext
}

// NewGateway constructs a Gateway. The authenticator is required for HTTP
// auth and WebSocket first-message auth. The cluster and actor-root
// arguments are required only for the WebSocket endpoint; pass nil for
// either to disable /ws (it will respond 503).
func NewGateway(authenticator auth.Authenticator, c *cluster.Cluster, root *actor.RootContext) *Gateway {
	return &Gateway{auth: authenticator, cluster: c, actorRoot: root}
}

// RegisterRoutes returns an http.Handler with all gateway routes registered.
// Routes use Go 1.22+ method+path patterns. The catch-all "/" pattern emits a
// JSON error envelope for unmatched paths, and the path-only "/login" pattern
// emits a JSON 405 for non-POST requests against /login. Go 1.22+ mux selects
// the most specific pattern, so "POST /login" wins over "/login" for POSTs.
//
// /ws is intentionally NOT wrapped with g.protected: WebSocket auth runs
// after upgrade as a first-frame protocol message, not via the
// Authorization header.
func (g *Gateway) RegisterRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", g.handleLogin)
	mux.HandleFunc("/login", g.handleMethodNotAllowed("POST"))
	mux.HandleFunc("GET /ws", g.handleWS)
	mux.HandleFunc("/ws", g.handleMethodNotAllowed("GET"))
	mux.HandleFunc("/", g.handleNotFound)
	return mux
}

// ListenAndServe starts the HTTP server on the given listen address.
func (g *Gateway) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, g.RegisterRoutes())
}
