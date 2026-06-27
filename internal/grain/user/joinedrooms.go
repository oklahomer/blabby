package user

import (
	"context"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/membershiprepo"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// JoinedRoomLoader hydrates the User grain's joined-rooms cache on activation
// from DB-authoritative membership, so the set survives passivation / node loss.
// It is read-only: the Room grain is the sole writer of membership.
//
// The implementation owns its own latency budget — callers pass a plain context
// and impose no deadline — mirroring the Room grain's loaders.
type JoinedRoomLoader interface {
	ListJoinedRooms(ctx context.Context, userID id.UserID) ([]domain.RoomRef, error)
}

// loadTimeout bounds a single activation-time joined-rooms read. It is owned here
// (the callee), not by the grain, so a stalled database cannot block an
// activation indefinitely.
const loadTimeout = 3 * time.Second

// membershipJoinedRoomLoader is the production JoinedRoomLoader: a read-only view
// of the caller's joined rooms via membershiprepo over the backend's pool.
type membershipJoinedRoomLoader struct {
	repo *membershiprepo.Repo
	pool postgres.Querier
}

// NewJoinedRoomLoader builds a JoinedRoomLoader over pool. It owns a
// membershiprepo.Repo internally; the User grain only reads its joined rooms.
func NewJoinedRoomLoader(pool postgres.Querier) JoinedRoomLoader {
	return membershipJoinedRoomLoader{repo: membershiprepo.New(), pool: pool}
}

func (l membershipJoinedRoomLoader) ListJoinedRooms(ctx context.Context, userID id.UserID) ([]domain.RoomRef, error) {
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()

	return l.repo.ListByUser(ctx, l.pool, userID)
}
