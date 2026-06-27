package user

import (
	"github.com/asynkron/protoactor-go/actor"

	commonpb "github.com/oklahomer/blabby/gen/common"
	"github.com/oklahomer/blabby/internal/id"
)

// Test-only seams. export_test.go is compiled only during `go test` so the
// production binary surface stays clean while tests can inspect private
// state and inject fakes.

// Connections returns a sorted snapshot of registered connection PIDs.
func (g *Grain) Connections() []*actor.PID { return g.state.connectionPIDs() }

// JoinedRooms returns a sorted snapshot of the joined-rooms set.
func (g *Grain) JoinedRooms() []id.RoomID { return g.state.joinedRoomIDs() }

// SetRoomClient injects a roomClient for tests; production code uses NewKind.
func (g *Grain) SetRoomClient(c roomClient) { g.rooms = c }

// SetSender injects a pidSender for tests.
func (g *Grain) SetSender(s pidSender) { g.send = s }

// SetDirectory injects the profile Directory for tests; production wires it
// via NewKind.
func (g *Grain) SetDirectory(d Directory) { g.directory = d }

// SetJoinedRoomLoader injects the joined-rooms loader for tests; production wires
// it via NewKind's WithJoinedRooms option. Call before Init so activation
// hydrates the cache from the stub.
func (g *Grain) SetJoinedRoomLoader(l JoinedRoomLoader) { g.joinedLoader = l }

// Self returns the UserRef the grain resolved for itself at activation, so
// tests can assert the seeded display name and its identity fallbacks.
func (g *Grain) Self() *commonpb.UserRef { return g.self }

// RoomClient is the test-visible alias of the unexported roomClient
// interface so test fakes outside this file can declare conformance.
type RoomClient = roomClient

// PIDSender is the test-visible alias of the unexported pidSender function
// type.
type PIDSender = pidSender
