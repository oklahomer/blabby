package gateway

import "github.com/oklahomer/blabby/internal/actor/connection"

// Test-only seams. export_test.go is compiled only during `go test` so the
// production binary surface stays clean while tests can adjust private
// state.

// SetHeartbeatCadence overrides the cadence handleWS passes to every new
// connection, so integration tests can drive pings and pong timeouts in
// milliseconds instead of the production default.
func (g *Gateway) SetHeartbeatCadence(c connection.HeartbeatCadence) { g.heartbeat = c }
