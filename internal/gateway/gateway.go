package gateway

import (
	"net/http"
	"strings"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
)

// Gateway is the HTTP entry point for the chat service. It translates
// between client-facing JSON / WebSocket frames and internal services.
type Gateway struct {
	auth         auth.Authenticator
	rooms        RoomDirectory
	users        UserResolver
	roomCreator  RoomCreator
	registration Registrar
	verification VerificationService
	maintenance  MaintenanceTrigger
	timeline     RoomTimeline
	cluster      *cluster.Cluster
	actorRoot    *actor.RootContext

	// userGrain is a test seam. Production construction in NewGateway
	// leaves it nil and userGrainFor falls through to
	// clusterUserGrainCaller. Non-nil only in tests.
	userGrain func(userID id.UserID) userGrainCaller
}

// Deps groups the Gateway's dependencies: the authenticator, the room directory
// (which resolves the opaque R… codes to internal RoomIDs for the HTTP routes),
// and the cluster client plus root for grain dispatch. Grouping them into one
// struct keeps the constructor stable as the gateway grows and lets call sites
// read by field name. A test may leave any field nil for a dependency the
// exercised routes do not touch.
type Deps struct {
	Authenticator auth.Authenticator
	Rooms         RoomDirectory
	Users         UserResolver
	RoomCreation  RoomCreator
	Registration  Registrar
	Verification  VerificationService
	Maintenance   MaintenanceTrigger
	Timeline      RoomTimeline
	Cluster       *cluster.Cluster
	ActorRoot     *actor.RootContext
}

// NewGateway constructs a Gateway from deps. Production wires all fields.
func NewGateway(deps Deps) *Gateway {
	return &Gateway{
		auth:         deps.Authenticator,
		rooms:        deps.Rooms,
		users:        deps.Users,
		roomCreator:  deps.RoomCreation,
		registration: deps.Registration,
		verification: deps.Verification,
		maintenance:  deps.Maintenance,
		timeline:     deps.Timeline,
		cluster:      deps.Cluster,
		actorRoot:    deps.ActorRoot,
	}
}

// RegisterRoutes returns an http.Handler with all gateway routes registered.
// Routes use Go 1.22+ method+path patterns. The catch-all "/" pattern emits a
// JSON error envelope for unmatched paths, and the path-only "/login" pattern
// emits a JSON 405 for non-POST requests against /login. Go 1.22+ mux selects
// the most specific pattern, so "POST /login" wins over "/login" for POSTs.
//
// /ws is intentionally NOT wrapped with g.requireAuth: WebSocket auth runs
// after upgrade as a first-frame protocol message, not via the
// Authorization header.
func (g *Gateway) RegisterRoutes() http.Handler {
	mux := http.NewServeMux()

	loginMethod, loginPath := splitMethodPath(endpointLogin)
	registerMethod, registerPath := splitMethodPath(endpointRegister)
	verifyMethod, verifyPath := splitMethodPath(endpointVerify)
	resendMethod, resendPath := splitMethodPath(endpointResendVerification)
	wsMethod, wsPath := splitMethodPath(endpointWS)

	mux.HandleFunc(endpointLogin, g.handleLogin)
	mux.HandleFunc(loginPath, g.handleMethodNotAllowed(loginMethod))
	mux.HandleFunc(endpointRegister, g.handleRegister)
	mux.HandleFunc(registerPath, g.handleMethodNotAllowed(registerMethod))
	mux.HandleFunc(endpointVerify, g.handleVerify)
	mux.HandleFunc(verifyPath, g.handleMethodNotAllowed(verifyMethod))
	mux.HandleFunc(endpointResendVerification, g.handleResendVerification)
	mux.HandleFunc(resendPath, g.handleMethodNotAllowed(resendMethod))
	mux.HandleFunc(endpointWS, g.handleWS)
	mux.HandleFunc(wsPath, g.handleMethodNotAllowed(wsMethod))
	mux.Handle(endpointRoomList, g.requireAuth(g.handleRoomList))
	mux.Handle(endpointRoomCreate, g.requireAuth(g.handleRoomCreate))
	mux.Handle(endpointRoomJoined, g.requireAuth(g.handleRoomJoined))
	mux.Handle(endpointRoomEvents, g.requireAuth(g.handleRoomEvents))
	mux.Handle(endpointRoomMembershipPut, g.requireAuth(g.handleRoomMembershipPut))
	mux.Handle(endpointRoomMembershipDelete, g.requireAuth(g.handleRoomMembershipDelete))
	mux.Handle(endpointRoomMessage, g.requireAuth(g.handleRoomSendMessage))
	mux.Handle(endpointRoomMemberRole, g.requireAuth(g.handleRoomMemberRolePut))
	mux.Handle(endpointRoomOwner, g.requireAuth(g.handleRoomOwnerPut))
	mux.HandleFunc("/", g.handleNotFound)
	return mux
}

// RegisterInternalRoutes returns the handler for the gateway's internal listener:
// operational endpoints (scheduled-job triggers) that must not be reachable from the
// public API. The caller serves this on a separate, network-restricted listener (see
// cmd/gateway), so these routes never share the public mux.
func (g *Gateway) RegisterInternalRoutes() http.Handler {
	mux := http.NewServeMux()

	gcMethod, gcPath := splitMethodPath(endpointPendingAccountGC)
	mux.HandleFunc(endpointPendingAccountGC, g.handlePendingAccountGC)
	mux.HandleFunc(gcPath, g.handleMethodNotAllowed(gcMethod))
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
func (g *Gateway) userGrainFor(userID id.UserID) userGrainCaller {
	if g.userGrain != nil {
		return g.userGrain(userID)
	}
	return newClusterUserGrainCaller(g.cluster).callerFor(userID)
}
