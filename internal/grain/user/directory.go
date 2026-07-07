package user

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// resolveTimeout bounds a single directory lookup. It is owned here (the callee),
// not the grain: the grain passes a deadline-free context, so a stalled database
// must not block a User grain's activation indefinitely.
const resolveTimeout = 3 * time.Second

// repoDirectory is the production Directory: it resolves a user's display name
// from service_user via the persistence user repo over the backend's pool, so every cluster member
// resolves the same profile from the one shared source.
type repoDirectory struct {
	repo *persistence.UserRepo
	pool postgres.Querier
}

// NewRepoDirectory builds a Directory over pool. It owns a persistence.UserRepo with a
// nil id source: resolving a profile reads accounts but never mints them.
func NewRepoDirectory(pool postgres.Querier) Directory {
	return repoDirectory{repo: persistence.NewUserRepo(nil), pool: pool}
}

func (d repoDirectory) Resolve(ctx context.Context, userID id.UserID) (id.UserRef, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	user, err := d.repo.FindByID(ctx, d.pool, userID)
	if errors.Is(err, persistence.ErrUserNotFound) {
		return id.UserRef{}, ErrProfileNotFound
	}
	if err != nil {
		return id.UserRef{}, fmt.Errorf("user: resolve directory: %w", err)
	}
	return id.NewUserRef(userID, user.PublicCode, user.DisplayName)
}
