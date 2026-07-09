// The event table is the append-only room timeline of messages and membership
// system events. The Room grain is the single appender per room, so the Snowflake
// event id is monotonic per room and orders the timeline. The Journal type writes
// this table; the timeline read path (pagination, full-text search) reads it.

package persistence

import "fmt"

// MemberEventKind discriminates the two membership timeline events. The zero value
// is invalid so a forgotten kind is rejected rather than silently mis-typed.
type MemberEventKind int

const (
	// MemberJoined records that a user joined a room.
	MemberJoined MemberEventKind = iota + 1
	// MemberLeft records that a user left a room.
	MemberLeft
)

// eventType maps the kind to its event_type SQL enum label, rejecting an
// unset/unknown kind.
func (k MemberEventKind) eventType() (string, error) {
	switch k {
	case MemberJoined:
		return "member_joined", nil
	case MemberLeft:
		return "member_left", nil
	default:
		return "", fmt.Errorf("persistence: unknown member event kind %d", int(k))
	}
}

// memberEventPayload is the JSONB payload for a membership event. It carries the
// actor's display name so a consumer renders the system line ("— alice joined —")
// without a lookup. The actor's id lives in the event's user_id column, not here.
// The actor's public code is not carried in this payload yet.
type memberEventPayload struct {
	DisplayName string `json:"display_name"`
}

// messagePayload is the JSONB payload for a message_posted event: the text only.
// The author's id lives in the event's user_id column; author display metadata
// is joined from service_user at read time, so the payload stays minimal. The
// text key is what the PGroonga message-search index covers (payload->>'text').
type messagePayload struct {
	Text string `json:"text"`
}
