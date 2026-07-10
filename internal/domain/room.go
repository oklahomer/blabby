package domain

import (
	"fmt"
	"strings"

	"github.com/oklahomer/blabby/internal/id"
)

// RoomStatus is a room's lifecycle state, mirroring the room_status SQL enum.
type RoomStatus string

const (
	// RoomStatusActive marks a listable, joinable room.
	RoomStatusActive RoomStatus = "active"
	// RoomStatusArchived marks a retired room: not listed and not addressable.
	RoomStatusArchived RoomStatus = "archived"
)

// ParseRoomStatus parses a raw room_status label (e.g. a DB enum value) into a
// RoomStatus, rejecting unknown values.
func ParseRoomStatus(raw string) (RoomStatus, error) {
	switch s := RoomStatus(raw); s {
	case RoomStatusActive, RoomStatusArchived:
		return s, nil
	default:
		return "", fmt.Errorf("domain: unknown room status %q", raw)
	}
}

// RoomRef is a denormalized snapshot of a room's reference metadata: its internal
// identity plus the client-facing fields. It is the small common currency that
// travels across grain messages, read models, and gateway rendering so a consumer
// can label a room without a per-use lookup.
//
// Only the id and public code are stable; the name and status may change. The
// metadata version lets a receiver ignore a stale snapshot. The room table and
// Room grain own the authoritative values.
//
// Construct it with [NewRoomRef]; a zero-value RoomRef is not valid. Requiring
// the public code keeps the internal id from ever having to stand in for it on
// the client wire.
type RoomRef struct {
	id              id.RoomID
	publicCode      id.PublicCode
	name            string
	status          RoomStatus
	metadataVersion int64
}

// RoomRefParams carries the already-parsed fields for [NewRoomRef]. Each field
// is parsed at its own boundary (row scan, proto decode) before it lands here;
// the constructor enforces only the cross-field ref invariants.
type RoomRefParams struct {
	ID              id.RoomID
	PublicCode      id.PublicCode
	Name            string
	Status          RoomStatus
	MetadataVersion int64
}

// NewRoomRef builds a RoomRef, requiring a non-zero id, a non-zero public code,
// a known status, and a non-blank name within [MaxRoomNameBytes]. The metadata
// version is opaque and accepted as-is.
func NewRoomRef(p RoomRefParams) (RoomRef, error) {
	if p.ID.IsZero() {
		return RoomRef{}, fmt.Errorf("domain: room ref: id must not be zero")
	}
	if p.PublicCode.IsZero() {
		return RoomRef{}, fmt.Errorf("domain: room ref: public code must not be zero")
	}
	if _, err := ParseRoomStatus(string(p.Status)); err != nil {
		return RoomRef{}, fmt.Errorf("domain: room ref: %w", err)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return RoomRef{}, fmt.Errorf("domain: room ref: name must not be blank")
	}
	if len(name) > MaxRoomNameBytes {
		return RoomRef{}, fmt.Errorf("domain: room ref: name exceeds %d bytes", MaxRoomNameBytes)
	}
	return RoomRef{
		id:              p.ID,
		publicCode:      p.PublicCode,
		name:            name,
		status:          p.Status,
		metadataVersion: p.MetadataVersion,
	}, nil
}

// ID returns the room's internal identity.
func (r RoomRef) ID() id.RoomID { return r.id }

// IsZero reports whether r is the zero value, which [NewRoomRef] never
// returns.
func (r RoomRef) IsZero() bool { return r == RoomRef{} }

// PublicCode returns the room's opaque public code (bare, no type letter).
func (r RoomRef) PublicCode() id.PublicCode { return r.publicCode }

// PublicID renders the room's client-facing R… code.
func (r RoomRef) PublicID() string { return r.publicCode.FormatRoom() }

// Name returns the room's display name.
func (r RoomRef) Name() string { return r.name }

// Status returns the room's lifecycle state as of this snapshot.
func (r RoomRef) Status() RoomStatus { return r.status }

// MetadataVersion returns the opaque monotonic version of this snapshot;
// receivers apply only snapshots newer than the one they hold.
func (r RoomRef) MetadataVersion() int64 { return r.metadataVersion }
