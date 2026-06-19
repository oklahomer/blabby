package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestNewPoolInvalidDSN covers the parse-error branch without needing a database:
// a syntactically invalid DSN fails before any connection is attempted.
func TestNewPoolInvalidDSN(t *testing.T) {
	ctx := context.Background()
	_, err := NewPool(ctx, Config{DSN: "postgres://localhost:notaport/db", MaxConns: 1})
	if err == nil {
		t.Fatal("expected error for invalid DSN, got nil")
	}
	if !strings.Contains(err.Error(), "parse db dsn") {
		t.Fatalf("error %q does not mention DSN parsing", err)
	}
}

// TestNewPoolIntegration connects to a real database and verifies the pool pings
// and runs a trivial query. It is skipped unless BLABBY_DATABASE_URL points at a
// reachable PostgreSQL instance (e.g. `make up`), so the default `go test` run
// stays hermetic.
func TestNewPoolIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(envDSNKey))
	if dsn == "" {
		t.Skipf("%s not set; skipping database integration test", envDSNKey)
	}

	cfg, err := newConfig(dsn, defaultMaxConns, defaultMaxConnIdleTime, defaultMaxConnLifetime)
	if err != nil {
		t.Fatalf("newConfig: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, cfg)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 returned %d, want 1", one)
	}
}
