package membershiprepo

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestMembershipRepoIntegration exercises the repo against a real database.
// Skipped unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance
// (e.g. `make up`) with the schema + dev seed applied (users 1-3, rooms 4-5,
// owner memberships 4->1 and 5->2). It adds and removes a membership it owns, so
// it is re-runnable.
func TestMembershipRepoIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BLABBY_DATABASE_URL"))
	if dsn == "" {
		t.Skip("BLABBY_DATABASE_URL not set; skipping database integration test")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, postgres.Config{
		DSN: dsn, MaxConns: 4, MaxConnIdleTime: time.Minute, MaxConnLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	repo := New()

	// Seeded: room 4's owner is user 1.
	members, err := repo.ListByRoom(ctx, pool, mustRoomID(t, 4))
	if err != nil {
		t.Fatalf("ListByRoom: %v", err)
	}
	owner, ok := findMember(members, mustUserID(t, 1))
	if !ok || owner.Role != domain.MembershipRoleOwner || owner.User.Name() != "alice" {
		t.Fatalf("room 4 members = %+v, want user 1 as owner 'alice'", members)
	}

	// Seeded: user 1 belongs to room 4 (active), surfaced as a RoomRef.
	joined, err := repo.ListByUser(ctx, pool, mustUserID(t, 1))
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if !containsRoom(joined, mustRoomID(t, 4)) {
		t.Fatalf("user 1 joined rooms = %v, want room 4", roomIDs(joined))
	}

	// Add user 3 (charlie) to room 4, confirm, then remove and confirm gone.
	t.Cleanup(func() {
		_ = repo.Remove(context.Background(), pool, mustRoomID(t, 4), mustUserID(t, 3))
	})
	if err := repo.Add(ctx, pool, mustRoomID(t, 4), mustUserRef(t, 3, "charlie"), domain.MembershipRoleMember); err != nil {
		t.Fatalf("Add: %v", err)
	}
	withCharlie, err := repo.ListByRoom(ctx, pool, mustRoomID(t, 4))
	if err != nil {
		t.Fatalf("ListByRoom after add: %v", err)
	}
	if added, ok := findMember(withCharlie, mustUserID(t, 3)); !ok || added.Role != domain.MembershipRoleMember {
		t.Fatalf("room 4 members after add = %+v, want user 3 as member", withCharlie)
	}

	if err := repo.Remove(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3)); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	withoutCharlie, err := repo.ListByRoom(ctx, pool, mustRoomID(t, 4))
	if err != nil {
		t.Fatalf("ListByRoom after remove: %v", err)
	}
	if _, ok := findMember(withoutCharlie, mustUserID(t, 3)); ok {
		t.Fatalf("room 4 still lists user 3 after remove: %+v", withoutCharlie)
	}

	// Removing an already-absent row is strict: it surfaces ErrMembershipNotFound
	// so a caller can fail closed on a cache/DB divergence.
	if err := repo.Remove(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3)); !errors.Is(err, ErrMembershipNotFound) {
		t.Fatalf("Remove(absent): got %v, want ErrMembershipNotFound", err)
	}
}

func findMember(members []Member, userID id.UserID) (Member, bool) {
	for _, m := range members {
		if m.User.ID() == userID {
			return m, true
		}
	}
	return Member{}, false
}

func containsRoom(rooms []domain.RoomRef, want id.RoomID) bool {
	for _, r := range rooms {
		if r.ID == want {
			return true
		}
	}
	return false
}

func roomIDs(rooms []domain.RoomRef) []int64 {
	out := make([]int64, len(rooms))
	for i, r := range rooms {
		out[i] = r.ID.Int64()
	}
	return out
}
