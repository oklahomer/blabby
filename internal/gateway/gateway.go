package gateway

import (
	"net/http"
	"strings"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"

	"github.com/oklahomer/blabby/internal/actor/connection"
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

	// metrics is the Prometheus scrape handler served at GET /metrics on the
	// internal listener. It is set only when the operator opted in (--metrics);
	// nil leaves the route unregistered so the path returns 404 — see
	// RegisterInternalRoutes.
	metrics http.Handler

	// heartbeat is the application ping/pong cadence handleWS passes to
	// every connection spawn. NewGateway sets the production default;
	// integration tests shorten it through the export_test seam.
	heartbeat connection.HeartbeatCadence

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

	// Metrics is the optional Prometheus scrape handler for the internal
	// listener. A nil value (the default) leaves GET /metrics unregistered, so
	// the route returns 404 and the feature stays off.
	Metrics http.Handler
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
		metrics:      deps.Metrics,
		heartbeat:    defaultHeartbeatCadence,
	}
}

// routeGroup declares one path's handlers and its Allow header. The full
// method+path pattern constants stay the single source of truth for routing;
// allow is a reviewed literal, deliberately declared rather than derived from
// the handlers, so a rejection-only registration (the HEAD /ws entry) never
// advertises itself. Go 1.22+ mux GET patterns also match HEAD, so GET paths
// advertise "GET, HEAD".
type routeGroup struct {
	allow  string
	routes []route
}

// route pairs one method+path pattern constant with its handler.
type route struct {
	pattern string
	handler http.Handler
}

// registerGroups wires each group's pattern→handler pairs plus one path-only
// fallback per group, answering every unlisted method with the JSON 405 and
// the group's Allow literal. The fallback path derives from the group's
// first pattern; Go 1.22+ mux selects the most specific pattern, so the
// method+path registrations win for their methods and everything else falls
// through to the path-only fallback.
func (g *Gateway) registerGroups(mux *http.ServeMux, groups []routeGroup) {
	for _, group := range groups {
		for _, r := range group.routes {
			mux.Handle(r.pattern, r.handler)
		}
		_, path := splitMethodPath(group.routes[0].pattern)
		mux.Handle(path, g.handleMethodNotAllowed(group.allow))
	}
}

// RegisterRoutes returns an http.Handler with all gateway routes registered.
// Each routeGroup carries one path; the catch-all "/" pattern emits a JSON
// error envelope for unmatched paths.
//
// /ws is intentionally NOT wrapped with g.requireAuth: WebSocket auth runs
// after upgrade as a first-frame protocol message, not via the
// Authorization header. It is also the one path that opts out of the
// GET-implies-HEAD rule: a WebSocket upgrade requires GET, and without the
// explicit HEAD registration the mux would route HEAD into the GET handler,
// where gorilla answers with a plain-text handshake error instead of the
// gateway's JSON 405.
func (g *Gateway) RegisterRoutes() http.Handler {
	mux := http.NewServeMux()
	g.registerGroups(mux, []routeGroup{
		{allow: "POST", routes: []route{{endpointLogin, http.HandlerFunc(g.handleLogin)}}},
		{allow: "POST", routes: []route{{endpointRegister, http.HandlerFunc(g.handleRegister)}}},
		{allow: "POST", routes: []route{{endpointVerify, http.HandlerFunc(g.handleVerify)}}},
		{allow: "POST", routes: []route{{endpointResendVerification, http.HandlerFunc(g.handleResendVerification)}}},
		{allow: "GET", routes: []route{
			{endpointWS, http.HandlerFunc(g.handleWS)},
			{"HEAD /ws", g.handleMethodNotAllowed("GET")},
		}},
		{allow: "GET, HEAD, POST", routes: []route{
			{endpointRoomList, g.requireAuth(g.handleRoomList)},
			{endpointRoomCreate, g.requireAuth(g.handleRoomCreate)},
		}},
		{allow: "GET, HEAD", routes: []route{{endpointRoomJoined, g.requireAuth(g.handleRoomJoined)}}},
		{allow: "GET, HEAD", routes: []route{{endpointRoomEvents, g.requireAuth(g.handleRoomEvents)}}},
		{allow: "PUT, DELETE", routes: []route{
			{endpointRoomMembershipPut, g.requireAuth(g.handleRoomMembershipPut)},
			{endpointRoomMembershipDelete, g.requireAuth(g.handleRoomMembershipDelete)},
		}},
		{allow: "POST", routes: []route{{endpointRoomMessage, g.requireAuth(g.handleRoomSendMessage)}}},
		{allow: "PUT", routes: []route{{endpointRoomMemberRole, g.requireAuth(g.handleRoomMemberRolePut)}}},
		{allow: "PUT", routes: []route{{endpointRoomOwner, g.requireAuth(g.handleRoomOwnerPut)}}},
	})
	mux.HandleFunc("/", g.handleNotFound)
	return mux
}

// endpointMetrics is the mux pattern for the Prometheus scrape handler on the
// internal listener. It is registered only when a metrics handler was injected
// (--metrics); otherwise the path falls through to the 404 catch-all.
const endpointMetrics = "GET /metrics"

// RegisterInternalRoutes returns the handler for the gateway's internal listener:
// operational endpoints (scheduled-job triggers) that must not be reachable from the
// public API. The caller serves this on a separate, network-restricted listener (see
// cmd/gateway), so these routes never share the public mux.
//
// GET /metrics is registered only when g.metrics is non-nil (the operator passed
// --metrics); with the feature off the path returns 404 like any unknown route.
func (g *Gateway) RegisterInternalRoutes() http.Handler {
	mux := http.NewServeMux()

	groups := []routeGroup{
		{allow: "POST", routes: []route{{endpointPendingAccountGC, http.HandlerFunc(g.handlePendingAccountGC)}}},
	}
	if g.metrics != nil {
		groups = append(groups, routeGroup{allow: "GET, HEAD", routes: []route{{endpointMetrics, g.metrics}}})
	}
	g.registerGroups(mux, groups)

	mux.HandleFunc("/", g.handleNotFound)
	return mux
}

// splitMethodPath splits a Go 1.22+ ServeMux pattern of the form
// "METHOD /path" into its method and path components. Patterns
// without a leading method return an empty method and the original
// string as the path.
func splitMethodPath(pattern string) (method, path string) {
	if m, p, ok := strings.Cut(pattern, " "); ok {
		return m, p
	}
	return "", pattern
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
