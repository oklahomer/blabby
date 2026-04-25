package gateway

import (
	"net/http"

	"github.com/oklahomer/blabby/internal/auth"
)

// Gateway is the HTTP entry point for the chat service. It translates between
// client-facing JSON and internal services. Additional dependencies (e.g. the
// cluster client) will be added in later stories.
type Gateway struct {
	auth auth.Authenticator
}

// NewGateway constructs a Gateway with the given authenticator.
func NewGateway(authenticator auth.Authenticator) *Gateway {
	return &Gateway{auth: authenticator}
}

// RegisterRoutes returns an http.Handler with all gateway routes registered.
// Routes use Go 1.22+ method+path patterns. The catch-all "/" pattern emits a
// JSON error envelope for unmatched paths, and the path-only "/login" pattern
// emits a JSON 405 for non-POST requests against /login. Go 1.22+ mux selects
// the most specific pattern, so "POST /login" wins over "/login" for POSTs.
func (g *Gateway) RegisterRoutes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", g.handleLogin)
	mux.HandleFunc("/login", g.handleLoginMethodNotAllowed)
	mux.HandleFunc("/", g.handleNotFound)
	return mux
}

// ListenAndServe starts the HTTP server on the given listen address.
func (g *Gateway) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, g.RegisterRoutes())
}
