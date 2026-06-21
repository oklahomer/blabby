// Package roomrepo persists and reads the room table: each chat room's identity
// (internal Snowflake RoomID plus a separate opaque public_code), its display
// name, creator, and lifecycle status. It is the gateway's authority for resolving
// a client-facing R… code to an internal RoomID and back, so no raw numeric room
// id ever crosses to the client.
//
// Like internal/persistence/workerlease, the repo issues raw parameterized SQL —
// its statements are fixed, and a query builder would only obscure them. Rows are
// parsed into typed value objects at the boundary (parse, don't validate), so the
// rest of the package handles RoomID/PublicCode, never bare ints or strings.
package roomrepo

import (
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// Room is the domain view of a room row. Its identifiers are parsed value objects,
// not raw primitives; construct one only through the repo, which parses the row at
// the persistence boundary.
type Room struct {
	ID          id.RoomID
	PublicCode  id.PublicCode
	DisplayName string
	CreatedBy   id.UserID
	Status      domain.RoomStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PublicID renders the room's client-facing R… code.
func (r Room) PublicID() string { return r.PublicCode.FormatRoom() }

// roomRow is the raw row scanned from Postgres: primitive columns in the fixed
// order the repo's SELECT/RETURNING lists them. toDomain parses it into a Room.
type roomRow struct {
	id          int64
	publicCode  string
	displayName string
	createdBy   int64
	status      string
	createdAt   time.Time
	updatedAt   time.Time
}

// toDomain parses a raw row into a Room, enforcing the id and status invariants at
// the boundary. A row that violates them (non-positive id, malformed public_code,
// unknown status) is a data-integrity error surfaced to the caller rather than
// silently trusted.
func (rr roomRow) toDomain() (Room, error) {
	roomID, err := id.NewRoomID(rr.id)
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: row id: %w", err)
	}
	createdBy, err := id.NewUserID(rr.createdBy)
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: row created_by: %w", err)
	}
	code, err := id.ParsePublicCode(rr.publicCode)
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: row public_code: %w", err)
	}
	status, err := domain.ParseRoomStatus(rr.status)
	if err != nil {
		return Room{}, fmt.Errorf("roomrepo: row status: %w", err)
	}
	return Room{
		ID:          roomID,
		PublicCode:  code,
		DisplayName: rr.displayName,
		CreatedBy:   createdBy,
		Status:      status,
		CreatedAt:   rr.createdAt,
		UpdatedAt:   rr.updatedAt,
	}, nil
}

// scannable is the Scan contract shared by pgx.Row (single-row QueryRow) and
// pgx.Rows (multi-row iteration), so one scanRoom helper serves both.
type scannable interface {
	Scan(dest ...any) error
}

// scanRoom reads one room row in the fixed column order and parses it into a Room.
// It returns the raw Scan error unwrapped (so callers can map pgx.ErrNoRows).
func scanRoom(s scannable) (Room, error) {
	var rr roomRow
	if err := s.Scan(
		&rr.id, &rr.publicCode, &rr.displayName, &rr.createdBy,
		&rr.status, &rr.createdAt, &rr.updatedAt,
	); err != nil {
		return Room{}, err
	}
	return rr.toDomain()
}
