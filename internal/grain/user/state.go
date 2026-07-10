package user

import (
	"cmp"
	"slices"

	"github.com/asynkron/protoactor-go/actor"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// userState holds a single User grain's in-memory state. It is mutated
// directly under the actor model's single-threaded guarantee, so the
// project's global immutability rule does not apply here.
//
// joinedRooms caches each joined room's RoomRef (public code, display name,
// status) keyed by its internal RoomID, so GetJoinedRooms answers without a
// per-request room lookup. The key is the routing-truth RoomID; the value's
// nested RoomRef.ID carries the same identity (an owner/key id repeated inside a
// nested ref is allowed). The cache is interim RoomRef-only; it gains role and
// joined-time once the membership relationship is cached alongside the room.
type userState struct {
	connections map[string]*actor.PID
	joinedRooms map[id.RoomID]domain.RoomRef
}

// newUserState builds an empty userState. The Grain calls this from Init.
func newUserState() userState {
	return userState{
		connections: map[string]*actor.PID{},
		joinedRooms: map[id.RoomID]domain.RoomRef{},
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
	slices.Sort(keys)
	out := make([]*actor.PID, len(keys))
	for i, k := range keys {
		out[i] = s.connections[k]
	}
	return out
}

// joinRoom records ref in the joined set, keyed by its own RoomID. Re-joining
// overwrites the cached ref so a re-join refreshes the room's metadata snapshot.
func (s *userState) joinRoom(ref domain.RoomRef) {
	s.joinedRooms[ref.ID()] = ref
}

// leaveRoom drops roomID from the joined set. No-op if absent.
func (s *userState) leaveRoom(roomID id.RoomID) {
	delete(s.joinedRooms, roomID)
}

// joinedRoomIDs returns a sorted snapshot of the joined-room ids.
func (s *userState) joinedRoomIDs() []id.RoomID {
	out := make([]id.RoomID, 0, len(s.joinedRooms))
	for roomID := range s.joinedRooms {
		out = append(out, roomID)
	}
	slices.SortFunc(out, func(a, b id.RoomID) int { return cmp.Compare(a.Int64(), b.Int64()) })
	return out
}

// joinedRoomRefs returns a snapshot of the cached room refs, sorted by RoomID
// for deterministic output.
func (s *userState) joinedRoomRefs() []domain.RoomRef {
	out := make([]domain.RoomRef, 0, len(s.joinedRooms))
	for _, ref := range s.joinedRooms {
		out = append(out, ref)
	}
	slices.SortFunc(out, func(a, b domain.RoomRef) int { return cmp.Compare(a.ID().Int64(), b.ID().Int64()) })
	return out
}
