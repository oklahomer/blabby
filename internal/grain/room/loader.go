package room

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// ErrRoomNotFound is the loader's contract for "no room carries this id". The
// Room grain treats it (and a non-active status) as a permanently invalid
// activation: the grain stays unloaded and every command returns ROOM_NOT_FOUND.
// It is the room package's own sentinel so the grain's activation logic does not
// depend on the persistence layer's error identity.
var ErrRoomNotFound = errors.New("room: room not found")

// RoomLoader hydrates a Room grain's reference metadata from the source of truth
// on activation. The grain owns the activated in-memory RoomRef; the database
// remains authoritative.
//
// An implementation returns ErrRoomNotFound when no room carries the id. Any
// other error is transient (e.g. the database is unreachable): the grain crashes
// its activation and the cluster re-activates on the next request.
//
// The implementation owns its own latency budget — callers pass a plain context
// and do not impose a deadline, so a slow database is the loader's concern to
// bound, not the grain's.
type RoomLoader interface {
	LoadRoom(ctx context.Context, roomID id.RoomID) (domain.RoomRef, error)
}

// loadTimeout bounds a single activation-time room load. It is owned here (the
// callee), not by the grain, so a stalled database cannot block an activation
// indefinitely.
const loadTimeout = 3 * time.Second

// roomRepoLoader is the production RoomLoader: a read-only view of the room table
// via the persistence room repo over the backend's pool. The backend reads a room
// to hydrate the grain but never mints one here, so the room repo's id source is
// unused — mirroring
// the gateway's read-only RoomDirectory.
type roomRepoLoader struct {
	repo *persistence.RoomRepo
	pool postgres.Querier
}

// NewRoomRepoLoader builds a RoomLoader over pool. It owns the persistence.RoomRepo
// internally with a nil id source, because activation reads rooms but never mints
// them — so callers never see the unused id source.
func NewRoomRepoLoader(pool postgres.Querier) RoomLoader {
	return roomRepoLoader{repo: persistence.NewRoomRepo(nil), pool: pool}
}

func (l roomRepoLoader) LoadRoom(ctx context.Context, roomID id.RoomID) (domain.RoomRef, error) {
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()

	room, err := l.repo.FindByID(ctx, l.pool, roomID)
	if errors.Is(err, persistence.ErrRoomNotFound) {
		return domain.RoomRef{}, ErrRoomNotFound
	}
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("room: load room: %w", err)
	}

	// MetadataVersion is UpdatedAt at microsecond precision (not milli), so two
	// metadata writes in the same millisecond do not collapse to one version
	// under a receiver's "ignore older" check. A monotonic revision column is the
	// eventual robust form; this is the interim stand-in.
	ref, err := domain.NewRoomRef(domain.RoomRefParams{
		ID:              room.ID,
		PublicCode:      room.PublicCode,
		Name:            room.DisplayName,
		Status:          room.Status,
		MetadataVersion: room.UpdatedAt.UnixMicro(),
	})
	if err != nil {
		return domain.RoomRef{}, fmt.Errorf("room: load room: %w", err)
	}
	return ref, nil
}
