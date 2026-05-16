package ids

import "fmt"

// RoomID identifies a chat room across the system. Its value is opaque
// to consumers; only [RoomID.String] exposes the underlying
// representation, and only [NewRoomID] can construct a non-zero value.
//
// A zero-value RoomID (RoomID{}) is not a valid identifier. Callers
// reaching one indicate a missing parse step at an earlier boundary —
// the type system cannot prevent the zero value from being declared,
// but the structural rules prevent the constructor from emitting one.
type RoomID struct{ value string }

// NewRoomID parses raw into a RoomID, applying the uniform structural
// rules documented on the package. On failure it returns the zero value
// and an error wrapping one of [ErrEmptyIdentifier],
// [ErrIdentifierTooLong], or [ErrIdentifierInvalidChar].
func NewRoomID(raw string) (RoomID, error) {
	v, err := parseIdentifier(raw)
	if err != nil {
		return RoomID{}, fmt.Errorf("room_id: %w", err)
	}
	return RoomID{value: v}, nil
}

// String returns the underlying identifier string. The empty string is
// returned only for the zero value.
func (id RoomID) String() string { return id.value }
