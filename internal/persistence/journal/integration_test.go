package journal

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestJournalIntegration exercises AppendMembership against a real database.
// Skipped unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance
// (e.g. `make up`) with the schema + dev seed applied (room 4, user 1). It mints a
// unique id per run and deletes the row it created, so it is re-runnable.
func TestJournalIntegration(t *testing.T) {
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

	// A time-based id avoids colliding with seed rows or a prior run.
	rawID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM event WHERE id = $1", rawID)
	})

	j := New(stubIDSource{id: rawID})
	eventID, occurredAt, err := j.AppendMembership(ctx, pool, mustRoomID(t, 4), mustUserRef(t, 1, "alice"), MemberJoined)
	if err != nil {
		t.Fatalf("AppendMembership: %v", err)
	}
	if eventID.Int64() != rawID {
		t.Fatalf("event id = %d, want %d", eventID.Int64(), rawID)
	}
	if occurredAt.IsZero() {
		t.Fatal("occurred_at is zero, want the server clock")
	}

	// The row is persisted with the member_joined type and the actor's name.
	var (
		gotType    string
		gotUserID  int64
		gotPayload string
	)
	err = pool.QueryRow(ctx,
		"SELECT type::text, user_id, payload->>'display_name' FROM event WHERE id = $1", rawID,
	).Scan(&gotType, &gotUserID, &gotPayload)
	if err != nil {
		t.Fatalf("read back event: %v", err)
	}
	if gotType != "member_joined" || gotUserID != 1 || gotPayload != "alice" {
		t.Errorf("event row = {type:%q user_id:%d display_name:%q}, want {member_joined 1 alice}", gotType, gotUserID, gotPayload)
	}
}
