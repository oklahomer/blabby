// Package postgres bootstraps the PostgreSQL connection pool (pgxpool) that the
// persistence layer is built on, and parses its configuration at the flag
// boundary.
//
// This package owns only the pool's lifecycle and configuration. The repositories
// that issue queries against the pool, the schema itself, and the domain types
// they map to live in sibling packages added in later phases. Keeping pool setup
// separate from query code mirrors the split between internal/clusterboot (cluster
// wiring) and the grains that run on the cluster.
package postgres

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// envDSNKey is the environment variable consulted for the default DSN. The
	// connection string carries the database password, so it is sourced from the
	// environment (a secret manager in production) rather than passed on the
	// command line where it would be visible in argv/ps. It is a named constant
	// so binaries and tests refer to it without repeating the literal.
	envDSNKey = "BLABBY_DATABASE_URL"

	// devDSN is the fallback DSN when neither --db-dsn nor BLABBY_DATABASE_URL is
	// set. It targets the local docker-compose postgres service (see
	// docker-compose.yml) so a developer can `make up` and connect with no extra
	// configuration. It is a development convenience only: production supplies a
	// real DSN via BLABBY_DATABASE_URL.
	devDSN = "postgres://blabby:blabby@localhost:5432/blabby?sslmode=disable"

	// Pool sizing defaults. pgxpool's own defaults are reasonable; these make the
	// blabby-specific choices explicit and tunable via flags.
	defaultMaxConns        = 10
	defaultMaxConnIdleTime = 5 * time.Minute
	defaultMaxConnLifetime = time.Hour

	// maxAllowedConns bounds --db-max-conns so the value fits pgxpool's int32
	// MaxConns field and stays within sane operational limits.
	maxAllowedConns = 1000
)

// Config is the parsed, validated PostgreSQL pool configuration. It is built once
// via BindFlags (the flag boundary) or newConfig and passed by value. Repositories
// receive the *pgxpool.Pool that NewPool produces from it, not the Config itself.
type Config struct {
	// DSN is the libpq/pgx connection string (e.g. postgres://user:pass@host/db).
	DSN string
	// MaxConns is the maximum number of connections the pool keeps open.
	MaxConns int32
	// MaxConnIdleTime is how long an idle connection lingers before it is closed.
	MaxConnIdleTime time.Duration
	// MaxConnLifetime is the maximum age of a connection before it is recycled.
	MaxConnLifetime time.Duration
}

// BindFlags registers the database flags on fs and returns a closure that builds
// and validates a Config from the parsed values. Call the closure after fs.Parse.
// Splitting registration from building lets a caller add its own flags to the same
// FlagSet (parse, don't validate at one boundary), mirroring clusterboot.BindFlags.
//
// The --db-dsn flag is registered with an empty display default so a
// BLABBY_DATABASE_URL value containing credentials is never printed by flag help.
// The returned closure resolves the effective DSN after parsing:
// explicit --db-dsn, then BLABBY_DATABASE_URL, then the local development DSN.
func BindFlags(fs *flag.FlagSet) func() (Config, error) {
	dsn := fs.String("db-dsn", "", "PostgreSQL connection DSN; defaults to $BLABBY_DATABASE_URL or a local dev DSN")
	maxConns := fs.Int("db-max-conns", defaultMaxConns, "maximum number of pooled database connections")
	maxConnIdleTime := fs.Duration("db-max-conn-idle-time", defaultMaxConnIdleTime, "maximum idle time before a pooled connection is closed")
	maxConnLifetime := fs.Duration("db-max-conn-lifetime", defaultMaxConnLifetime, "maximum lifetime of a pooled connection before it is recycled")

	return func() (Config, error) {
		effectiveDSN := resolveDSN(fs, *dsn)
		return newConfig(effectiveDSN, *maxConns, *maxConnIdleTime, *maxConnLifetime)
	}
}

func resolveDSN(fs *flag.FlagSet, flagValue string) string {
	if flagWasSet(fs, "db-dsn") {
		return flagValue
	}
	if envDSN := strings.TrimSpace(os.Getenv(envDSNKey)); envDSN != "" {
		return envDSN
	}
	return devDSN
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	seen := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			seen = true
		}
	})
	return seen
}

// newConfig validates the raw flag values into a Config (parse, don't validate).
// It enforces only the basic invariants the pool needs to exist: a non-empty DSN
// and sane pool sizing. The DSN's syntactic validity is checked by pgxpool when
// NewPool parses it, and reachability is proven by NewPool's ping.
func newConfig(dsn string, maxConns int, maxConnIdleTime, maxConnLifetime time.Duration) (Config, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return Config{}, errors.New("--db-dsn must not be empty (set it or $BLABBY_DATABASE_URL)")
	}
	if maxConns < 1 || maxConns > maxAllowedConns {
		return Config{}, fmt.Errorf("--db-max-conns must be in 1-%d (got %d)", maxAllowedConns, maxConns)
	}
	if maxConnIdleTime < 0 {
		return Config{}, fmt.Errorf("--db-max-conn-idle-time must not be negative (got %s)", maxConnIdleTime)
	}
	if maxConnLifetime < 0 {
		return Config{}, fmt.Errorf("--db-max-conn-lifetime must not be negative (got %s)", maxConnLifetime)
	}

	return Config{
		DSN:             dsn,
		MaxConns:        int32(maxConns),
		MaxConnIdleTime: maxConnIdleTime,
		MaxConnLifetime: maxConnLifetime,
	}, nil
}
