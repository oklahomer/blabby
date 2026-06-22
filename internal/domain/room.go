package domain

import (
	"fmt"

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
// Only ID and PublicCode are stable; Name and Status may change. MetadataVersion
// lets a receiver ignore a stale snapshot. The room table and Room grain own the
// authoritative values.
type RoomRef struct {
	ID              id.RoomID
	PublicCode      id.PublicCode
	Name            string
	Status          RoomStatus
	MetadataVersion int64
}

// PublicID renders the room's client-facing R… code.
func (r RoomRef) PublicID() string { return r.PublicCode.FormatRoom() }
