package room

import (
	"sort"
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

// maxRecentMessages bounds the in-memory ring buffer of recent messages
// retained by a Room grain. Tunable in a later story; persistence is out of
// scope for Phase 1.
const maxRecentMessages = 100

// chatMessage is the in-memory representation of a posted message held by
// the Room grain. The timestamp is a domain time.Time for readability and
// type safety; conversion to the project's canonical int64 Unix-milliseconds
// wire format (architecture.md timestamp rule) happens at the proto
// boundary in events.go and in PostMessage's response.
type chatMessage struct {
	senderID  id.UserID
	text      string
	timestamp time.Time
}

// roomState holds a single Room grain's in-memory state. It is mutated
// directly under the actor model's single-threaded guarantee — the project's
// global immutability rule does not apply to grain state (architecture.md
// "Grain State Management").
type roomState struct {
	// members caches each member's denormalized UserRef (id + display name),
	// keyed by id. The Room owns this cache so fan-out can label events
	// locally — never a synchronous lookup back to the User grain.
	members           map[id.UserID]id.UserRef
	recentMessages    []chatMessage
	maxRecentMessages int
}

// newRoomState builds an empty roomState with the package-default message
// bound. The caller (the Grain) is responsible for invoking this from Init.
func newRoomState() roomState {
	return roomState{
		members:           map[id.UserID]id.UserRef{},
		maxRecentMessages: maxRecentMessages,
	}
}

// addMember records ref as a new member, keyed by its id. Returns false if the
// user was already a member; in that case the cache is unchanged (use
// refreshMember to update an existing member's name).
func (s *roomState) addMember(ref id.UserRef) bool {
	if _, ok := s.members[ref.ID()]; ok {
		return false
	}
	s.members[ref.ID()] = ref
	return true
}

// refreshMember updates an existing member's cached UserRef (e.g. a newer
// display name carried on a message). It is a no-op if the user is not a
// member, so it never resurrects a removed one.
func (s *roomState) refreshMember(ref id.UserRef) {
	if _, ok := s.members[ref.ID()]; ok {
		s.members[ref.ID()] = ref
	}
}

// memberRef returns the cached UserRef for userID and whether the user is
// currently a member.
func (s *roomState) memberRef(userID id.UserID) (id.UserRef, bool) {
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
		// Drop the oldest message. Copy is intentionally simple — Phase 1
		// recent-message buffers are small (default 100); a circular buffer
		// is overkill until measurement says otherwise.
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
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
