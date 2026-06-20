package user

import (
	"sort"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/id"
)

// userState holds a single User grain's in-memory state. It is mutated
// directly under the actor model's single-threaded guarantee, so the
// project's global immutability rule does not apply here.
type userState struct {
	connections map[string]*actor.PID
	joinedRooms map[id.RoomID]struct{}
}

// newUserState builds an empty userState. The Grain calls this from Init.
func newUserState() userState {
	return userState{
		connections: map[string]*actor.PID{},
		joinedRooms: map[id.RoomID]struct{}{},
	}
}

// pidKey derives the connections-map key from a PID. We compose the key
// from the public Address/Id fields directly rather than calling
// pid.String(), which would populate an internal protobuf cache field on
// the PID and make tests that compare PIDs by reflect.DeepEqual flake.
func pidKey(pid *actor.PID) string {
	return pid.GetAddress() + "/" + pid.GetId()
}

// addConnection records pid in the active connections set. Re-adding the
// same PID is a no-op; a different PID is a separate entry. The PID is
// the connection's identity — see ADR-012.
func (s *userState) addConnection(pid *actor.PID) {
	s.connections[pidKey(pid)] = pid
}

// removeConnection drops pid from the active connections set. No-op if
// absent. Called from the Terminated handler when protoactor's death-watch
// reports that a registered UserConnection actor has stopped.
func (s *userState) removeConnection(pid *actor.PID) {
	delete(s.connections, pidKey(pid))
}

// connectionPIDs returns a freshly allocated snapshot of the current
// connection PIDs sorted by their canonical string form. Sorting is not
// required for correct delivery, but a stable order keeps fan-out tests
// deterministic.
func (s *userState) connectionPIDs() []*actor.PID {
	if len(s.connections) == 0 {
		return nil
	}
	keys := make([]string, 0, len(s.connections))
	for k := range s.connections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*actor.PID, len(keys))
	for i, k := range keys {
		out[i] = s.connections[k]
	}
	return out
}

// joinRoom records roomID in the joined set. No-op if already present.
func (s *userState) joinRoom(roomID id.RoomID) {
	s.joinedRooms[roomID] = struct{}{}
}

// leaveRoom drops roomID from the joined set. No-op if absent.
func (s *userState) leaveRoom(roomID id.RoomID) {
	delete(s.joinedRooms, roomID)
}

// joinedRoomIDs returns a sorted snapshot of the joined-rooms set.
func (s *userState) joinedRoomIDs() []id.RoomID {
	out := make([]id.RoomID, 0, len(s.joinedRooms))
	for roomID := range s.joinedRooms {
		out = append(out, roomID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Int64() < out[j].Int64() })
	return out
}
