package gateway

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/journal"
	"github.com/oklahomer/blabby/internal/persistence/membershiprepo"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestRoomCreationIntegration exercises the creation service against a real
// database: the room row, the owner membership, and the founding member_joined
// event must commit together. Skipped unless BLABBY_DATABASE_URL points at a
// reachable PostgreSQL with the schema + dev seed applied (user 1 = alice). It
// cleans up the rows it creates.
func TestRoomCreationIntegration(t *testing.T) {
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
	// Registered before the row cleanup below so it runs after it (LIFO).
	t.Cleanup(pool.Close)

	base := time.Now().UnixNano()
	ids := &incrementingIDSource{next: base}
	svc := NewRoomCreationService(
		persistence.NewRoomRepo(ids),
		persistence.NewUserRepo(ids),
		membershiprepo.New(),
		journal.New(ids),
		postgres.NewTransactor(pool),
	)

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		// FK order: events and memberships reference the room.
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM event WHERE room_id > $1 AND room_id <= $2", base, base+100)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM room_membership WHERE room_id > $1 AND room_id <= $2", base, base+100)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM room WHERE id > $1 AND id <= $2", base, base+100)
	})

	actor := mustUserID(t, "1") // seed user alice
	name, err := domain.NewRoomName("Creation Test Room")
	if err != nil {
		t.Fatalf("NewRoomName: %v", err)
	}

	info, err := svc.CreateRoom(ctx, actor, name)
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if !strings.HasPrefix(info.PublicID(), "R") || info.Name != "Creation Test Room" {
		t.Fatalf("info = %+v, want an R… code and the given name", info)
	}

	var status, role, eventType string
	var eventCount int
	if err := pool.QueryRow(ctx, "SELECT status::text FROM room WHERE id = $1", info.ID.Int64()).Scan(&status); err != nil {
		t.Fatalf("room row: %v", err)
	}
	if status != "active" {
		t.Errorf("room status = %q, want active", status)
	}
	if err := pool.QueryRow(ctx, "SELECT role::text FROM room_membership WHERE room_id = $1 AND user_id = $2", info.ID.Int64(), actor.Int64()).Scan(&role); err != nil {
		t.Fatalf("owner membership row: %v", err)
	}
	if role != "owner" {
		t.Errorf("creator role = %q, want owner", role)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*), max(type::text) FROM event WHERE room_id = $1", info.ID.Int64()).Scan(&eventCount, &eventType); err != nil {
		t.Fatalf("event rows: %v", err)
	}
	if eventCount != 1 || eventType != "member_joined" {
		t.Errorf("events = %d of type %q, want exactly one member_joined", eventCount, eventType)
	}
}
