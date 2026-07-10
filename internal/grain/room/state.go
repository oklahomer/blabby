package room

import (
	"sort"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// maxRecentMessages bounds the in-memory recent-message window a Room grain
// retains. The window is the room's hot tier in the no-cache architecture
// (ADR-008): no production read path consumes it yet — history reads are
// served from PostgreSQL by the gateway (ADR-007) — but hot-window reads are
// reserved to be answered by the grain, so the buffer is deliberately kept
// despite being write-only today.
const maxRecentMessages = 100

// chatMessage is the in-memory representation of a posted message held by
// the Room grain. The timestamp is a domain time.Time for readability and
// type safety; conversion to the project's canonical int64 Unix-milliseconds
// wire format happens at the proto boundary in events.go and in
// PostMessage's response.
type chatMessage struct {
	senderID  id.UserID
	text      string
	timestamp time.Time
}

// roomState holds a single Room grain's in-memory state. It is mutated
// directly: the actor model's single-threaded guarantee makes the grain the
// sole writer, so copy-on-write ceremony would add allocation without adding
// safety.
type roomState struct {
	// ref is the room's reference metadata, hydrated from the source of truth on
	// activation. refLoaded reports whether hydration succeeded for an active
	// room; until it does, the grain is invalid and every command is rejected.
	ref       domain.RoomRef
	refLoaded bool
	// members caches each member's denormalized UserRef (id + public code +
	// display name), keyed by id. The Room owns this cache so fan-out can label
	// events locally — never a synchronous lookup back to the User grain.
	members           map[id.UserID]domain.UserRef
	recentMessages    []chatMessage
	maxRecentMessages int
}

// newRoomState builds an empty roomState with the package-default message
// bound. The caller (the Grain) is responsible for invoking this from Init.
func newRoomState() roomState {
	return roomState{
		members:           map[id.UserID]domain.UserRef{},
		maxRecentMessages: maxRecentMessages,
	}
}

// loadRoom caches the activated reference metadata and marks the grain loaded.
func (s *roomState) loadRoom(ref domain.RoomRef) {
	s.ref = ref
	s.refLoaded = true
}

// isLoaded reports whether the grain hydrated an active room on activation. A
// command on an unloaded grain (absent or archived room) is rejected.
func (s *roomState) isLoaded() bool {
	return s.refLoaded
}

// roomRef returns the cached reference metadata. It is only meaningful once
// isLoaded reports true; callers guard on that before reading it.
func (s *roomState) roomRef() domain.RoomRef {
	return s.ref
}

// addMember records ref as a new member, keyed by its id. Returns false if the
// user was already a member; in that case the cache is unchanged (use
// refreshMember to update an existing member's name).
func (s *roomState) addMember(ref domain.UserRef) bool {
	if _, ok := s.members[ref.ID()]; ok {
		return false
	}
	s.members[ref.ID()] = ref
	return true
}

// refreshMember updates an existing member's cached UserRef (e.g. a newer
// display name carried on a message). It is a no-op if the user is not a
// member, so it never resurrects a removed one.
func (s *roomState) refreshMember(ref domain.UserRef) {
	if _, ok := s.members[ref.ID()]; ok {
		s.members[ref.ID()] = ref
	}
}

// memberRef returns the cached UserRef for userID and whether the user is
// currently a member.
func (s *roomState) memberRef(userID id.UserID) (domain.UserRef, bool) {
	ref, ok := s.members[userID]
	return ref, ok
}

// removeMember erases userID from the member set. Returns false if the user
// was not a member; in that case the state is unchanged.
func (s *roomState) removeMember(userID id.UserID) bool {
	if _, ok := s.members[userID]; !ok {
		return false
	}
	delete(s.members, userID)
	return true
}

// isMember reports whether userID is currently a member of the room.
func (s *roomState) isMember(userID id.UserID) bool {
	_, ok := s.members[userID]
	return ok
}

// recordMessage appends msg to the recent-message buffer, evicting the oldest
// entry once the bound is reached. If maxRecentMessages is misconfigured to a
// non-positive value, the bound falls back to the package default to prevent
// unbounded buffer growth.
func (s *roomState) recordMessage(msg chatMessage) {
	bound := s.maxRecentMessages
	if bound <= 0 {
		bound = maxRecentMessages
	}
	if len(s.recentMessages) >= bound {
		// Drop the oldest message. Copy is intentionally simple — the
		// window is small (default 100); a circular buffer is overkill
		// until measurement says otherwise.
		s.recentMessages = append(s.recentMessages[:0], s.recentMessages[1:]...)
	}
	s.recentMessages = append(s.recentMessages, msg)
}

// memberIDs returns a freshly allocated, lexicographically sorted snapshot
// of the current member set. Sorting yields deterministic fan-out order in
// tests and across nodes; allocating prevents iteration-during-mutation
// bugs when a fan-out loop also mutates the member set.
func (s *roomState) memberIDs() []id.UserID {
	out := make([]id.UserID, 0, len(s.members))
	for userID := range s.members {
		out = append(out, userID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Int64() < out[j].Int64() })
	return out
}
