package roomrepo

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestRoomRepoIntegration exercises the repo against a real database. Skipped
// unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance (e.g.
// `make up`), which has the schema and dev seed (rooms 4/5, user 1) applied. It
// mints a unique id per run and deletes the row it created, so it is re-runnable.
func TestRoomRepoIntegration(t *testing.T) {
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

	// A time-based id avoids colliding with the seed rows or a prior run.
	rawID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM room WHERE id = $1", rawID)
	})

	repo := New(&stubIDSource{id: rawID})
	created, err := repo.Create(ctx, pool, CreateParams{
		Name:      mustRoomName(t, "Integration Room"),
		CreatedBy: mustUserID(t, 1), // seed user alice
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID.Int64() != rawID {
		t.Fatalf("created id = %d, want %d", created.ID.Int64(), rawID)
	}
	if created.Status != domain.RoomStatusActive {
		t.Fatalf("created status = %q, want active", created.Status)
	}
	if !strings.HasPrefix(created.PublicID(), "R") {
		t.Fatalf("created PublicID = %q, want an R… code", created.PublicID())
	}

	// FindByPublicCode resolves the opaque code back to the same room.
	found, err := repo.FindByPublicCode(ctx, pool, created.PublicCode)
	if err != nil {
		t.Fatalf("FindByPublicCode: %v", err)
	}
	if found.ID != created.ID {
		t.Fatalf("FindByPublicCode id = %d, want %d", found.ID.Int64(), created.ID.Int64())
	}

	// An unknown code is reported as not found, not as a zero-value room.
	other, _ := id.NewPublicCode()
	if _, err := repo.FindByPublicCode(ctx, pool, other); err != ErrRoomNotFound {
		t.Fatalf("FindByPublicCode(unknown) err = %v, want ErrRoomNotFound", err)
	}

	// An archived room is not addressable: neither read surfaces it, so an
	// archived room can never leak to a client.
	archivedID := rawID + 1
	archivedCode, _ := id.NewPublicCode()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM room WHERE id = $1", archivedID)
	})
	if _, err := pool.Exec(ctx,
		"INSERT INTO room (id, public_code, display_name, created_by, status) VALUES ($1, $2, $3, $4, 'archived')",
		archivedID, archivedCode.String(), "Archived Room", int64(1)); err != nil {
		t.Fatalf("seed archived room: %v", err)
	}
	if _, err := repo.FindByPublicCode(ctx, pool, archivedCode); err != ErrRoomNotFound {
		t.Fatalf("FindByPublicCode(archived) err = %v, want ErrRoomNotFound", err)
	}
	withArchived, err := repo.ListByIDs(ctx, pool, []id.RoomID{created.ID, mustRoomID(t, archivedID)})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if containsRoom(withArchived, mustRoomID(t, archivedID)) {
		t.Fatalf("ListByIDs returned the archived room; want it filtered out (got %v)", roomIDs(withArchived))
	}
	if !containsRoom(withArchived, created.ID) {
		t.Fatalf("ListByIDs dropped the active room; got %v", roomIDs(withArchived))
	}

	// ListByIDs returns exactly the rooms whose ids were requested.
	byIDs, err := repo.ListByIDs(ctx, pool, []id.RoomID{created.ID, mustRoomID(t, 4)})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if !containsRoom(byIDs, created.ID) || !containsRoom(byIDs, mustRoomID(t, 4)) {
		t.Fatalf("ListByIDs = %v, want both the created room and seed room 4", roomIDs(byIDs))
	}

	// ListActive includes the freshly created active room.
	active, err := repo.ListActive(ctx, pool, 0)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if !containsRoom(active, created.ID) {
		t.Fatalf("ListActive missing the created room; got %v", roomIDs(active))
	}
}

func containsRoom(rooms []Room, want id.RoomID) bool {
	for _, r := range rooms {
		if r.ID == want {
			return true
		}
	}
	return false
}

func roomIDs(rooms []Room) []int64 {
	out := make([]int64, len(rooms))
	for i, r := range rooms {
		out[i] = r.ID.Int64()
	}
	return out
}
