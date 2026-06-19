package postgres

import (
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

// bindAndParse drives the BindFlags closure the way a binary's parseConfig does:
// register the flags on a fresh FlagSet, parse args, then build the Config.
func bindAndParse(t *testing.T, args []string) (Config, error) {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	build := BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	return build()
}

// TestNewConfig exercises the validation boundary directly, independent of flag
// parsing and the environment.
func TestNewConfig(t *testing.T) {
	const validDSN = "postgres://u:p@localhost:5432/db?sslmode=disable"

	tests := []struct {
		name            string
		dsn             string
		maxConns        int
		maxConnIdleTime time.Duration
		maxConnLifetime time.Duration
		wantErr         bool
		errMatch        string
		wantDSN         string
	}{
		{
			name:            "valid",
			dsn:             validDSN,
			maxConns:        10,
			maxConnIdleTime: time.Minute,
			maxConnLifetime: time.Hour,
			wantDSN:         validDSN,
		},
		{
			name:            "dsn is trimmed",
			dsn:             "  " + validDSN + "  ",
			maxConns:        1,
			maxConnIdleTime: 0,
			maxConnLifetime: 0,
			wantDSN:         validDSN,
		},
		{
			name:     "empty dsn rejected",
			dsn:      "",
			maxConns: 10,
			wantErr:  true,
			errMatch: "db-dsn",
		},
		{
			name:     "whitespace dsn rejected",
			dsn:      "   ",
			maxConns: 10,
			wantErr:  true,
			errMatch: "db-dsn",
		},
		{
			name:     "max conns below range rejected",
			dsn:      validDSN,
			maxConns: 0,
			wantErr:  true,
			errMatch: "db-max-conns",
		},
		{
			name:     "max conns above range rejected",
			dsn:      validDSN,
			maxConns: maxAllowedConns + 1,
			wantErr:  true,
			errMatch: "db-max-conns",
		},
		{
			name:            "negative idle time rejected",
			dsn:             validDSN,
			maxConns:        10,
			maxConnIdleTime: -time.Second,
			wantErr:         true,
			errMatch:        "db-max-conn-idle-time",
		},
		{
			name:            "negative lifetime rejected",
			dsn:             validDSN,
			maxConns:        10,
			maxConnLifetime: -time.Second,
			wantErr:         true,
			errMatch:        "db-max-conn-lifetime",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newConfig(tc.dsn, tc.maxConns, tc.maxConnIdleTime, tc.maxConnLifetime)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got config %+v", got)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("error %q does not contain %q", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.DSN != tc.wantDSN {
				t.Errorf("DSN = %q, want %q", got.DSN, tc.wantDSN)
			}
			if got.MaxConns != int32(tc.maxConns) {
				t.Errorf("MaxConns = %d, want %d", got.MaxConns, tc.maxConns)
			}
		})
	}
}

// TestBindFlagsDSNResolution covers the --db-dsn precedence: an explicit flag wins
// over BLABBY_DATABASE_URL, which in turn overrides the built-in dev default.
func TestBindFlagsDSNResolution(t *testing.T) {
	const envDSN = "postgres://env:env@envhost:5432/envdb"
	const flagDSN = "postgres://flag:flag@flaghost:5432/flagdb"

	t.Run("falls back to dev DSN when env unset", func(t *testing.T) {
		t.Setenv(envDSNKey, "")
		cfg, err := bindAndParse(t, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DSN != devDSN {
			t.Errorf("DSN = %q, want dev default %q", cfg.DSN, devDSN)
		}
	})

	t.Run("uses env DSN when set and no flag", func(t *testing.T) {
		t.Setenv(envDSNKey, envDSN)
		cfg, err := bindAndParse(t, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DSN != envDSN {
			t.Errorf("DSN = %q, want env %q", cfg.DSN, envDSN)
		}
	})

	t.Run("flag overrides env", func(t *testing.T) {
		t.Setenv(envDSNKey, envDSN)
		cfg, err := bindAndParse(t, []string{"--db-dsn", flagDSN})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DSN != flagDSN {
			t.Errorf("DSN = %q, want flag %q", cfg.DSN, flagDSN)
		}
	})

	t.Run("explicit empty flag is rejected", func(t *testing.T) {
		t.Setenv(envDSNKey, envDSN)
		if _, err := bindAndParse(t, []string{"--db-dsn", ""}); err == nil {
			t.Fatal("expected error for empty --db-dsn, got nil")
		}
	})
}

func TestBindFlagsHelpDoesNotExposeEnvDSN(t *testing.T) {
	const secretDSN = "postgres://secret:password@db.example.com:5432/blabby"
	t.Setenv(envDSNKey, secretDSN)

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	var out strings.Builder
	fs.SetOutput(&out)
	_ = BindFlags(fs)

	fs.PrintDefaults()

	if got := out.String(); strings.Contains(got, secretDSN) {
		t.Fatalf("flag help exposed %s in output:\n%s", envDSNKey, got)
	}
}

// TestBindFlagsPoolFlags confirms the pool-sizing flags reach the Config and that
// the defaults apply when they are omitted.
func TestBindFlagsPoolFlags(t *testing.T) {
	t.Setenv(envDSNKey, "")

	t.Run("defaults", func(t *testing.T) {
		cfg, err := bindAndParse(t, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.MaxConns != defaultMaxConns {
			t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, defaultMaxConns)
		}
		if cfg.MaxConnIdleTime != defaultMaxConnIdleTime {
			t.Errorf("MaxConnIdleTime = %s, want %s", cfg.MaxConnIdleTime, defaultMaxConnIdleTime)
		}
		if cfg.MaxConnLifetime != defaultMaxConnLifetime {
			t.Errorf("MaxConnLifetime = %s, want %s", cfg.MaxConnLifetime, defaultMaxConnLifetime)
		}
	})

	t.Run("overrides", func(t *testing.T) {
		cfg, err := bindAndParse(t, []string{
			"--db-max-conns", "20",
			"--db-max-conn-idle-time", "90s",
			"--db-max-conn-lifetime", "2h",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.MaxConns != 20 {
			t.Errorf("MaxConns = %d, want 20", cfg.MaxConns)
		}
		if cfg.MaxConnIdleTime != 90*time.Second {
			t.Errorf("MaxConnIdleTime = %s, want 90s", cfg.MaxConnIdleTime)
		}
		if cfg.MaxConnLifetime != 2*time.Hour {
			t.Errorf("MaxConnLifetime = %s, want 2h", cfg.MaxConnLifetime)
		}
	})
}
