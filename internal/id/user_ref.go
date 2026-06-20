package id

import (
	"fmt"
	"strings"
)

// maxUserNameBytes bounds a display name. Names are server-sourced today, but
// parsing at the boundary still rejects an absurd value rather than trust it.
const maxUserNameBytes = 256

// UserRef pairs a user's stable identity with their display name — a
// denormalized snapshot that travels with room messages and events so the
// recipient can label "who" without a lookup, and that the Room grain caches.
//
// Construct it with [NewUserRef]; a zero-value UserRef is not valid. The name
// is the display label only; identity is and remains the [UserID].
type UserRef struct {
	id   UserID
	name string
}

// NewUserRef builds a UserRef from an already-parsed UserID and a display
// name. The name is trimmed of surrounding whitespace and must be non-empty
// and within maxUserNameBytes; the UserID must be non-zero (parsed at its own
// boundary via [NewUserID]).
func NewUserRef(userID UserID, name string) (UserRef, error) {
	if userID == (UserID{}) {
		return UserRef{}, fmt.Errorf("user_ref: id must not be zero")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return UserRef{}, fmt.Errorf("user_ref: name must not be empty")
	}
	if len(trimmed) > maxUserNameBytes {
		return UserRef{}, fmt.Errorf("user_ref: name exceeds %d bytes", maxUserNameBytes)
	}
	return UserRef{id: userID, name: trimmed}, nil
}

// ID returns the user's identity.
func (u UserRef) ID() UserID { return u.id }

// Name returns the user's display name.
func (u UserRef) Name() string { return u.name }
