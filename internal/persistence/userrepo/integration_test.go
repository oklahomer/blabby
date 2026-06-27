package userrepo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestUserRepoIntegration exercises the repo against a real database. Skipped
// unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance (e.g.
// `make up`), which has the schema and dev seed (user 1 = alice) applied. It
// mints a unique id per run and deletes the row it created, so it is re-runnable.
func TestUserRepoIntegration(t *testing.T) {
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

	// A time-based id and unique email/handle avoid colliding with the seed rows
	// or a prior run.
	rawID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM service_user WHERE id = $1", rawID)
	})
	mail := mustMailAddress(t, fmt.Sprintf("itest-%d@example.com", rawID))
	handle := mustHandle(t, fmt.Sprintf("itest_%d", rawID))

	repo := New(&stubIDSource{id: rawID})
	created, err := repo.Create(ctx, pool, CreateParams{
		MailAddress:  mail,
		Handle:       handle,
		DisplayName:  "Integration User",
		PasswordHash: []byte("$2a$12$integration.placeholder.hash"),
		Status:       domain.UserStatusPending,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID.Int64() != rawID {
		t.Fatalf("created id = %d, want %d", created.ID.Int64(), rawID)
	}
	if created.Status != domain.UserStatusPending {
		t.Fatalf("created status = %q, want pending", created.Status)
	}
	if !strings.HasPrefix(created.PublicID(), "U") {
		t.Fatalf("created PublicID = %q, want a U… code", created.PublicID())
	}

	// Each read resolves the freshly created account.
	byEmail, err := repo.FindByEmail(ctx, pool, mail)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if byEmail.ID != created.ID {
		t.Fatalf("FindByEmail id = %d, want %d", byEmail.ID.Int64(), created.ID.Int64())
	}
	byHandle, err := repo.FindByHandle(ctx, pool, handle)
	if err != nil {
		t.Fatalf("FindByHandle: %v", err)
	}
	if byHandle.ID != created.ID {
		t.Fatalf("FindByHandle id = %d, want %d", byHandle.ID.Int64(), created.ID.Int64())
	}
	byID, err := repo.FindByID(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if byID.MailAddress != mail || len(byID.PasswordHash) == 0 {
		t.Fatalf("FindByID = %+v, want the stored email and a non-empty hash", byID)
	}

	// ResolveByPublicCode maps the opaque code back to the internal id.
	resolved, err := repo.ResolveByPublicCode(ctx, pool, created.PublicCode)
	if err != nil {
		t.Fatalf("ResolveByPublicCode: %v", err)
	}
	if resolved != created.ID {
		t.Fatalf("ResolveByPublicCode = %d, want %d", resolved.Int64(), created.ID.Int64())
	}

	// SetStatus flips pending → active, and the change is visible on the next read.
	if err := repo.SetStatus(ctx, pool, created.ID, domain.UserStatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	activated, err := repo.FindByID(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("FindByID after SetStatus: %v", err)
	}
	if activated.Status != domain.UserStatusActive {
		t.Fatalf("status after SetStatus = %q, want active", activated.Status)
	}

	// The seed account is loginable by email and carries its fixed id.
	alice, err := repo.FindByEmail(ctx, pool, mustMailAddress(t, "alice@example.com"))
	if err != nil {
		t.Fatalf("FindByEmail(seed alice): %v", err)
	}
	if alice.ID.Int64() != 1 || alice.Status != domain.UserStatusActive {
		t.Fatalf("seed alice = %+v, want id 1, active", alice)
	}

	// An unknown code and id are reported as not found, not as zero-value rows.
	other, _ := id.NewPublicCode()
	if _, err := repo.ResolveByPublicCode(ctx, pool, other); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("ResolveByPublicCode(unknown) err = %v, want ErrUserNotFound", err)
	}
	if _, err := repo.FindByEmail(ctx, pool, mustMailAddress(t, "nobody@example.com")); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("FindByEmail(unknown) err = %v, want ErrUserNotFound", err)
	}
}
