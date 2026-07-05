package id

import (
	"fmt"
	"strings"
)

// maxUserNameBytes bounds a display name. Names are server-sourced today, but
// parsing at the boundary still rejects an absurd value rather than trust it.
const maxUserNameBytes = 256

// UserRef pairs a user's stable identity with the public reference fields that
// travel with room messages and events so a recipient can label "who" without a
// lookup, and that the Room grain caches. It carries both the internal [UserID]
// (never shown to a client) and the opaque [PublicCode] (the U… the client
// sees) — the two are always resolved together, so a valid ref has both.
//
// Construct it with [NewUserRef]; a zero-value UserRef is not valid. The name is
// the display label only; identity is and remains the [UserID].
type UserRef struct {
	id         UserID
	publicCode PublicCode
	name       string
}

// NewUserRef builds a UserRef from an already-parsed UserID, its opaque public
// code, and a display name. The name is trimmed and must be non-empty and within
// maxUserNameBytes; the UserID and PublicCode must both be non-zero (each parsed
// at its own boundary). Requiring the public code keeps the internal id from
// ever having to stand in for it on the client wire.
func NewUserRef(userID UserID, publicCode PublicCode, name string) (UserRef, error) {
	if userID == (UserID{}) {
		return UserRef{}, fmt.Errorf("user_ref: id must not be zero")
	}
	if publicCode == (PublicCode{}) {
		return UserRef{}, fmt.Errorf("user_ref: public code must not be zero")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return UserRef{}, fmt.Errorf("user_ref: name must not be empty")
	}
	if len(trimmed) > maxUserNameBytes {
		return UserRef{}, fmt.Errorf("user_ref: name exceeds %d bytes", maxUserNameBytes)
	}
	return UserRef{id: userID, publicCode: publicCode, name: trimmed}, nil
}

// ID returns the user's internal identity.
func (u UserRef) ID() UserID { return u.id }

// PublicCode returns the user's opaque public code (bare, no type letter).
func (u UserRef) PublicCode() PublicCode { return u.publicCode }

// PublicID renders the user's client-facing U… code.
func (u UserRef) PublicID() string { return u.publicCode.FormatUser() }

// Name returns the user's display name.
func (u UserRef) Name() string { return u.name }
