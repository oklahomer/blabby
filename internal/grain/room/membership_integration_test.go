package room_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/grain/room"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// stubIDSource satisfies the persistence event id source; the role operations under test never
// mint an event id, so it only has to exist.
type stubIDSource struct{ next int64 }

func (s *stubIDSource) Next() (int64, error) {
	s.next++
	return s.next, nil
}

// TestMembershipStore_RoleOps_Integration exercises the production
// MembershipStore's role operations — the policy check and the mutation run in
// one transaction — against a real database. Skipped unless BLABBY_DATABASE_URL
// points at a reachable PostgreSQL with the schema + dev seed applied (room 5's
// owner is user 2). It restores the room's seed state on cleanup.
func TestMembershipStore_RoleOps_Integration(t *testing.T) {
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
	// Registered before the seed-restoring cleanup below so it runs after it
	// (LIFO); a defer would close the pool before any t.Cleanup could use it.
	t.Cleanup(pool.Close)

	repo := persistence.NewMembershipRepo()
	store := room.NewMembershipStore(pool, &stubIDSource{})

	// Room 5's seeded owner is user 2; users 1 and 3 are the scratch members.
	// This test stays off room 4, which the persistence membership integration test
	// mutates concurrently when test packages run in parallel.
	roomID, err := id.ParseRoomID("5")
	if err != nil {
		t.Fatalf("ParseRoomID: %v", err)
	}
	owner := mustUserID(t, "2")
	alice := mustUserID(t, "1")
	charlie := mustUserID(t, "3")

	// restoreSeedState returns room 5 to its seeded shape: the scratch members
	// gone and user 2 owning the room. Deleting the scratch rows first matters —
	// if a failed run left ownership with alice, re-promoting the seed owner
	// before that delete would transiently violate the one-owner index.
	restoreSeedState := func(ctx context.Context) error {
		if _, err := pool.Exec(ctx, "DELETE FROM room_membership WHERE room_id = $1 AND user_id IN ($2, $3)", roomID.Int64(), alice.Int64(), charlie.Int64()); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, "UPDATE room_membership SET role = 'owner' WHERE room_id = $1 AND user_id = $2", roomID.Int64(), owner.Int64())
		return err
	}

	// Restore the seed state first so leftovers from an earlier failed run can't
	// skew this one, and again on cleanup so a failure mid-test (say, after
	// ownership moved to alice) never leaves the shared room ownerless for later
	// runs.
	if err := restoreSeedState(ctx); err != nil {
		t.Fatalf("restore seed state: %v", err)
	}
	t.Cleanup(func() {
		if err := restoreSeedState(context.Background()); err != nil {
			t.Errorf("restore seed state on cleanup: %v", err)
		}
	})
	for _, u := range []struct {
		uid  id.UserID
		name string
	}{{alice, "alice"}, {charlie, "charlie"}} {
		code, err := id.NewPublicCode()
		if err != nil {
			t.Fatalf("NewPublicCode: %v", err)
		}
		ref, err := domain.NewUserRef(u.uid, code, u.name)
		if err != nil {
			t.Fatalf("NewUserRef: %v", err)
		}
		if err := repo.Add(ctx, pool, roomID, ref, domain.MembershipRoleMember); err != nil {
			t.Fatalf("Add %s: %v", u.name, err)
		}
	}

	mustRole := func(uid id.UserID, want domain.MembershipRole, when string) {
		t.Helper()
		role, err := repo.GetRole(ctx, pool, roomID, uid)
		if err != nil || role != want {
			t.Fatalf("%s: GetRole(%s) = %q, %v; want %q", when, uid, role, err, want)
		}
	}

	// A plain member may not change roles.
	if err := store.RecordRoleChange(ctx, roomID, charlie, alice, domain.MembershipRoleAdmin); !errors.Is(err, room.ErrRolePermissionDenied) {
		t.Fatalf("member changing roles: got %v, want ErrRolePermissionDenied", err)
	}
	mustRole(alice, domain.MembershipRoleMember, "after refused change")

	// The owner promotes alice; the admin alice promotes charlie.
	if err := store.RecordRoleChange(ctx, roomID, owner, alice, domain.MembershipRoleAdmin); err != nil {
		t.Fatalf("owner promotes alice: %v", err)
	}
	mustRole(alice, domain.MembershipRoleAdmin, "after owner promotion")
	if err := store.RecordRoleChange(ctx, roomID, alice, charlie, domain.MembershipRoleAdmin); err != nil {
		t.Fatalf("admin promotes charlie: %v", err)
	}
	mustRole(charlie, domain.MembershipRoleAdmin, "after admin promotion")

	// No admin may touch the owner's role or transfer ownership.
	if err := store.RecordRoleChange(ctx, roomID, alice, owner, domain.MembershipRoleMember); !errors.Is(err, room.ErrRolePermissionDenied) {
		t.Fatalf("admin demoting the owner: got %v, want ErrRolePermissionDenied", err)
	}
	if err := store.RecordOwnershipTransfer(ctx, roomID, alice, charlie); !errors.Is(err, room.ErrRolePermissionDenied) {
		t.Fatalf("admin transferring ownership: got %v, want ErrRolePermissionDenied", err)
	}

	// Transferring to the current owner is an idempotent no-op.
	if err := store.RecordOwnershipTransfer(ctx, roomID, owner, owner); err != nil {
		t.Fatalf("transfer to self: %v", err)
	}
	mustRole(owner, domain.MembershipRoleOwner, "after no-op transfer")

	// A real transfer moves the owner role and demotes the old owner to admin;
	// transferring back restores the seed.
	if err := store.RecordOwnershipTransfer(ctx, roomID, owner, alice); err != nil {
		t.Fatalf("transfer to alice: %v", err)
	}
	mustRole(alice, domain.MembershipRoleOwner, "after transfer")
	mustRole(owner, domain.MembershipRoleAdmin, "after transfer")
	if err := store.RecordOwnershipTransfer(ctx, roomID, alice, owner); err != nil {
		t.Fatalf("transfer back: %v", err)
	}
	mustRole(owner, domain.MembershipRoleOwner, "after restore")
}
