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
	// Registered before the row cleanups below so it runs after them (LIFO); a
	// defer would close the pool before any t.Cleanup could use it.
	t.Cleanup(pool.Close)

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

	// The seeded owner (user 1) cannot be removed: the guarded delete keeps the
	// row and reports the reason.
	if err := repo.Remove(ctx, pool, mustRoomID(t, 4), mustUserID(t, 1)); !errors.Is(err, ErrOwnerCannotLeave) {
		t.Fatalf("Remove(owner): got %v, want ErrOwnerCannotLeave", err)
	}
	if role, err := repo.GetRole(ctx, pool, mustRoomID(t, 4), mustUserID(t, 1)); err != nil || role != domain.MembershipRoleOwner {
		t.Fatalf("GetRole(owner) = %q, %v; the refused delete must keep the owner row", role, err)
	}

	// Role lifecycle on a scratch member: member -> admin -> transfer receives
	// ownership (old owner demotes to admin) -> transfer back restores the seed.
	if err := repo.Add(ctx, pool, mustRoomID(t, 4), mustUserRef(t, 3, "charlie"), domain.MembershipRoleMember); err != nil {
		t.Fatalf("Add for role lifecycle: %v", err)
	}
	if err := repo.UpdateRole(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3), domain.MembershipRoleAdmin); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if role, err := repo.GetRole(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3)); err != nil || role != domain.MembershipRoleAdmin {
		t.Fatalf("GetRole after update = %q, %v; want admin", role, err)
	}

	if err := repo.TransferOwnership(ctx, pool, mustRoomID(t, 4), mustUserID(t, 1), mustUserID(t, 3)); err != nil {
		t.Fatalf("TransferOwnership: %v", err)
	}
	if role, err := repo.GetRole(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3)); err != nil || role != domain.MembershipRoleOwner {
		t.Fatalf("GetRole(new owner) = %q, %v; want owner", role, err)
	}
	if role, err := repo.GetRole(ctx, pool, mustRoomID(t, 4), mustUserID(t, 1)); err != nil || role != domain.MembershipRoleAdmin {
		t.Fatalf("GetRole(old owner) = %q, %v; want admin (kept management rights)", role, err)
	}

	// Restore the seed state: hand ownership back, drop user 1 back to owner via
	// the transfer, then remove the scratch member.
	if err := repo.TransferOwnership(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3), mustUserID(t, 1)); err != nil {
		t.Fatalf("TransferOwnership back: %v", err)
	}
	if role, err := repo.GetRole(ctx, pool, mustRoomID(t, 4), mustUserID(t, 1)); err != nil || role != domain.MembershipRoleOwner {
		t.Fatalf("GetRole(restored owner) = %q, %v; want owner", role, err)
	}
	if err := repo.Remove(ctx, pool, mustRoomID(t, 4), mustUserID(t, 3)); err != nil {
		t.Fatalf("Remove scratch member: %v", err)
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
