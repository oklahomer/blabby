package gateway

import (
	"net/http"
	"strings"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	"github.com/oklahomer/blabby/internal/auth"
)

// Gateway is the HTTP entry point for the chat service. It translates
// between client-facing JSON / WebSocket frames and internal services.
type Gateway struct {
	auth      auth.Authenticator
	cluster   *cluster.Cluster
	actorRoot *actor.RootContext

	// userGrain is a test seam. Production construction in NewGateway
	// leaves it nil and userGrainFor falls through to
	// clusterUserGrainCaller. Non-nil only in tests.
	userGrain func(userID string) userGrainCaller
}

// NewGateway constructs a Gateway. All three arguments are required.
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

	loginMethod, loginPath := splitMethodPath(endpointLogin)
	wsMethod, wsPath := splitMethodPath(endpointWS)

	mux.HandleFunc(endpointLogin, g.handleLogin)
	mux.HandleFunc(loginPath, g.handleMethodNotAllowed(loginMethod))
	mux.HandleFunc(endpointWS, g.handleWS)
	mux.HandleFunc(wsPath, g.handleMethodNotAllowed(wsMethod))
	mux.Handle(endpointRoomList, g.protected(g.handleRoomList))
	mux.Handle(endpointRoomJoined, g.protected(g.handleRoomJoined))
	mux.Handle(endpointRoomJoin, g.protected(g.handleRoomJoin))
	mux.Handle(endpointRoomLeave, g.protected(g.handleRoomLeave))
	mux.Handle(endpointRoomMessage, g.protected(g.handleRoomSendMessage))
	mux.HandleFunc("/", g.handleNotFound)
	return mux
}

// splitMethodPath splits a Go 1.22+ ServeMux pattern of the form
// "METHOD /path" into its method and path components. Patterns
// without a leading method return an empty method and the original
// string as the path.
func splitMethodPath(pattern string) (method, path string) {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		return pattern[:i], pattern[i+1:]
	}
	return "", pattern
}

// ListenAndServe starts the HTTP server on the given listen address.
func (g *Gateway) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, g.RegisterRoutes())
}

// userGrainFor returns the userGrainCaller that handlers should use to
// dispatch User-grain RPCs for the given user. In tests, the userGrain
// field on Gateway is set to inject a fake. In production it is nil and
// the call falls through to a per-user adapter over the generated
// cluster client.
func (g *Gateway) userGrainFor(userID string) userGrainCaller {
	if g.userGrain != nil {
		return g.userGrain(userID)
	}
	return newClusterUserGrainCaller(g.cluster).callerFor(userID)
}
