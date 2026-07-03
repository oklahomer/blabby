package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// endpointPendingAccountGC triggers one pending-account GC sweep. It is served only
// on the gateway's internal listener (see RegisterInternalRoutes), never the public
// API listener, and is unauthenticated — access is restricted at the network layer.
const endpointPendingAccountGC = "POST /internal/jobs/pending-account-gc"

// PendingAccountGCResponse is the JSON body returned by the GC trigger endpoint.
type PendingAccountGCResponse struct {
	// Accepted is true when this trigger started a sweep, false when one was already
	// running (the trigger was coalesced).
	Accepted bool `json:"accepted"`
	// Reason explains a non-accepted result.
	Reason string `json:"reason,omitempty"`
}

// handlePendingAccountGC asks the singleton maintenance grain to run a sweep. The
// grain coalesces concurrent triggers, so the endpoint reports whether this call
// started a sweep (202) or found one already running (200). It does not wait for the
// sweep to finish; a failure to reach the grain is a 503.
func (g *Gateway) handlePendingAccountGC(w http.ResponseWriter, r *http.Request) {
	accepted, err := g.maintenance.TriggerPendingAccountGC(r.Context())
	if err != nil {
		slog.Error("internal job: pending-account GC trigger failed", "error", err.Error())
		WriteErrorResponse(w, http.StatusServiceUnavailable, ErrServiceUnavailable("maintenance unavailable"))
		return
	}

	resp := PendingAccountGCResponse{Accepted: accepted}
	status := http.StatusAccepted
	if !accepted {
		status = http.StatusOK
		resp.Reason = "already_running"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("failed to write internal job response", "error", err)
	}
}
