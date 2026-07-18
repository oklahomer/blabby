package domain

import (
	"fmt"
	"strings"

	"github.com/oklahomer/blabby/internal/id"
)

// maxUserNameBytes bounds a display name. Names are server-sourced today, but
// parsing at the boundary still rejects an absurd value rather than trust it.
const maxUserNameBytes = 256

// UserStatus is a user account's lifecycle state, mirroring the user_status SQL enum.
type UserStatus string

const (
	// UserStatusPending marks an account awaiting email verification.
	UserStatusPending UserStatus = "pending"
	// UserStatusActive marks a verified, usable account.
	UserStatusActive UserStatus = "active"
	// UserStatusDisabled marks a deactivated account.
	UserStatusDisabled UserStatus = "disabled"
)

// ParseUserStatus parses a raw user_status label (e.g. a DB enum value) into a
// UserStatus, rejecting unknown values.
func ParseUserStatus(raw string) (UserStatus, error) {
	switch s := UserStatus(raw); s {
	case UserStatusPending, UserStatusActive, UserStatusDisabled:
		return s, nil
	default:
		return "", fmt.Errorf("domain: unknown user status %q", raw)
	}
}

// UserRef is a denormalized snapshot of a user's public reference metadata. It
// pairs the user's stable identity with the public reference fields that travel
// with room messages and events so a recipient can label "who" without a
// per-message lookup, and that the Room grain caches. It carries both the
// internal [id.UserID] (never shown to a client) and the opaque [id.PublicCode]
// (the U… the client sees) — the two are always resolved together, so a valid
// ref has both.
//
// UserRef carries only public profile metadata; private account fields (email,
// handle) belong to a separate account view, because UserRef travels through
// room fan-out. Construct it with [NewUserRef]; a zero-value UserRef is not
// valid. The name is the display label only; identity is and remains the
// [id.UserID].
type UserRef struct {
	id         id.UserID
	publicCode id.PublicCode
	name       string
}

// NewUserRef builds a UserRef from an already-parsed UserID, its opaque public
// code, and a display name. The name is NFC-normalized and trimmed, and must be
// non-empty and within maxUserNameBytes; the UserID and PublicCode must both be
// non-zero (each parsed at its own boundary). Requiring the public code keeps
// the internal id from ever having to stand in for it on the client wire.
func NewUserRef(userID id.UserID, publicCode id.PublicCode, name string) (UserRef, error) {
	if userID.IsZero() {
		return UserRef{}, fmt.Errorf("domain: user ref: id must not be zero")
	}
	if publicCode.IsZero() {
		return UserRef{}, fmt.Errorf("domain: user ref: public code must not be zero")
	}
	trimmed := strings.TrimSpace(normalizeNFC(name))
	if trimmed == "" {
		return UserRef{}, fmt.Errorf("domain: user ref: name must not be empty")
	}
	if len(trimmed) > maxUserNameBytes {
		return UserRef{}, fmt.Errorf("domain: user ref: name exceeds %d bytes", maxUserNameBytes)
	}
	return UserRef{id: userID, publicCode: publicCode, name: trimmed}, nil
}

// ID returns the user's internal identity.
func (u UserRef) ID() id.UserID { return u.id }

// IsZero reports whether u is the zero value, which [NewUserRef] never
// returns.
func (u UserRef) IsZero() bool { return u == UserRef{} }

// PublicCode returns the user's opaque public code (bare, no type letter).
func (u UserRef) PublicCode() id.PublicCode { return u.publicCode }

// PublicID renders the user's client-facing U… code.
func (u UserRef) PublicID() string { return u.publicCode.FormatUser() }

// Name returns the user's display name.
func (u UserRef) Name() string { return u.name }
