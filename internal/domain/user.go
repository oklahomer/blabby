package domain

import (
	"fmt"

	"github.com/oklahomer/blabby/internal/id"
)

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

// UserRef is a denormalized snapshot of a user's public reference metadata: their
// internal identity plus the client-facing fields. It is the small common currency
// that travels with room messages and events so a recipient can label "who"
// without a per-message lookup.
//
// UserRef carries only public profile metadata; private account fields (email,
// handle) belong to a separate account view, because UserRef travels through room
// fan-out. Only ID and PublicCode are stable; Name and Status may change.
type UserRef struct {
	ID              id.UserID
	PublicCode      id.PublicCode
	Name            string
	Status          UserStatus
	MetadataVersion int64
}

// PublicID renders the user's client-facing U… code.
func (u UserRef) PublicID() string { return u.PublicCode.FormatUser() }
