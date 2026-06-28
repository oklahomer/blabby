package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/userrepo"
	"github.com/oklahomer/blabby/internal/persistence/verifyrepo"
)

// incrementingIDSource hands out unique, monotonically increasing ids seeded from a
// time-based base, so a registration integration run never collides with the seed
// rows or a prior run.
type incrementingIDSource struct{ next int64 }

func (s *incrementingIDSource) Next() (int64, error) {
	s.next++
	return s.next, nil
}

// TestRegistrationIntegration exercises the registration service against a real
// database, validating the real transaction, constraints, and challenge insert.
// Skipped unless BLABBY_DATABASE_URL points at a reachable PostgreSQL with the
// schema applied. It cleans up the rows it creates, so it is re-runnable.
func TestRegistrationIntegration(t *testing.T) {
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

	base := time.Now().UnixNano()
	t.Cleanup(func() {
		// The email_verification FK cascades, so deleting the user removes its challenge.
		_, _ = pool.Exec(context.Background(), "DELETE FROM service_user WHERE id > $1 AND id <= $2", base, base+100)
	})

	sender := &recordingVerificationSender{}
	svc := NewRegistrationService(
		userrepo.New(&incrementingIDSource{next: base}),
		verifyrepo.New(),
		sender,
		postgres.NewTransactor(pool),
		RegistrationPolicy{PinTTL: 10 * time.Minute, ResendMinInterval: time.Minute, MaxResendCount: 5, CollisionRetries: 3},
	)

	suffix := fmt.Sprintf("%d", base)
	mail := mustIntegrationMail(t, "itest-"+suffix+"@example.com")
	handle := mustIntegrationHandle(t, "itest_"+suffix[len(suffix)-8:])

	// A fresh registration creates a pending account, stores a hashed challenge, and
	// dispatches the PIN.
	result, err := svc.Register(ctx, RegisterParams{MailAddress: mail, Handle: handle, Password: "supersecret12"})
	if err != nil {
		t.Fatalf("Register(new): %v", err)
	}
	if !strings.HasPrefix(result.PublicCode, "U") {
		t.Fatalf("PublicCode = %q, want a U… code", result.PublicCode)
	}
	if sender.calls != 1 || sender.to.String() != mail.String() {
		t.Fatalf("sender got %d calls to %q, want 1 to %q", sender.calls, sender.to.String(), mail.String())
	}

	users := userrepo.New(nil)
	created, err := users.FindByEmail(ctx, pool, mail)
	if err != nil {
		t.Fatalf("FindByEmail after register: %v", err)
	}
	if created.Status != domain.UserStatusPending {
		t.Fatalf("status = %q, want pending", created.Status)
	}
	verify := verifyrepo.New()
	challenge, err := verify.FindByUser(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("FindByUser (verification): %v", err)
	}
	if len(challenge.PinHash) == 0 {
		t.Fatal("verification row has an empty pin_hash")
	}

	// A different email reusing the same handle is rejected.
	otherMail := mustIntegrationMail(t, "itest2-"+suffix+"@example.com")
	if _, err := svc.Register(ctx, RegisterParams{MailAddress: otherMail, Handle: handle, Password: "supersecret12"}); !errors.Is(err, userrepo.ErrHandleTaken) {
		t.Fatalf("Register(duplicate handle) = %v, want ErrHandleTaken", err)
	}
}

func mustIntegrationMail(t *testing.T, raw string) domain.MailAddress {
	t.Helper()
	m, err := domain.NewMailAddress(raw)
	if err != nil {
		t.Fatalf("NewMailAddress(%q): %v", raw, err)
	}
	return m
}

func mustIntegrationHandle(t *testing.T, raw string) domain.Handle {
	t.Helper()
	h, err := domain.NewHandle(raw)
	if err != nil {
		t.Fatalf("NewHandle(%q): %v", raw, err)
	}
	return h
}
