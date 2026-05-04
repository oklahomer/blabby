package user

import "github.com/asynkron/protoactor-go/actor"

// Test-only seams. export_test.go is compiled only during `go test` so the
// production binary surface stays clean while tests can inspect private
// state and inject fakes.

// Connections returns a sorted snapshot of registered connection PIDs.
func (g *Grain) Connections() []*actor.PID { return g.state.connectionPIDs() }

// JoinedRooms returns a sorted snapshot of the joined-rooms set.
func (g *Grain) JoinedRooms() []string { return g.state.joinedRoomIDs() }

// SetRoomClient injects a roomClient for tests; production code uses NewKind.
func (g *Grain) SetRoomClient(c roomClient) { g.rooms = c }

// SetSender injects a pidSender for tests.
func (g *Grain) SetSender(s pidSender) { g.send = s }

// RoomClient is the test-visible alias of the unexported roomClient
// interface so test fakes outside this file can declare conformance.
type RoomClient = roomClient

// PIDSender is the test-visible alias of the unexported pidSender function
// type.
type PIDSender = pidSender
