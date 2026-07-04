package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

type userDirectoryQuerier struct {
	queryRow func(sql string, args ...any) pgx.Row
	exec     func(sql string, args ...any) (pgconn.CommandTag, error)
}

var _ postgres.Querier = (*userDirectoryQuerier)(nil)

func (q *userDirectoryQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if q.exec == nil {
		return pgconn.CommandTag{}, fmt.Errorf("unexpected Exec: %s", sql)
	}
	return q.exec(sql, args...)
}

func (q *userDirectoryQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query: %s", sql)
}

func (q *userDirectoryQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if q.queryRow == nil {
		return userDirectoryRow{scan: func(...any) error {
			return fmt.Errorf("unexpected QueryRow: %s", sql)
		}}
	}
	return q.queryRow(sql, args...)
}

type userDirectoryRow struct {
	scan func(dest ...any) error
}

func (r userDirectoryRow) Scan(dest ...any) error {
	return r.scan(dest...)
}

func scanUserDirectoryRow(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("fake scan: %d destinations, %d values", len(dest), len(values))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = values[i].(int64)
		case *string:
			*d = values[i].(string)
		case *[]byte:
			*d = values[i].([]byte)
		case *time.Time:
			*d = values[i].(time.Time)
		default:
			return fmt.Errorf("fake scan: unsupported destination %T", dest[i])
		}
	}
	return nil
}

func activeUserDirectoryRow(t *testing.T, userID int64, code id.PublicCode, mailAddress, password string) pgx.Row {
	t.Helper()
	return userDirectoryRowWithStatus(t, userID, code, mailAddress, password, "active")
}

func userDirectoryRowWithStatus(t *testing.T, userID int64, code id.PublicCode, mailAddress, password, status string) pgx.Row {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	now := time.Unix(0, 0).UTC()
	return userDirectoryRow{scan: func(dest ...any) error {
		return scanUserDirectoryRow(dest, []any{
			userID,
			code.String(),
			mailAddress,
			"alice",
			"alice",
			"Alice",
			hash,
			status,
			now,
			now,
		})
	}}
}

func TestUserRepoDirectoryVerifyCredentialsClassifiesAccountStatus(t *testing.T) {
	const password = "hunter2"
	code, err := id.ParsePublicCode("0123456789")
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}

	cases := []struct {
		name     string
		status   string
		password string
		wantErr  error
	}{
		{
			// The password proved account ownership, so the pending state may be
			// revealed to guide the user to verification.
			name: "pending with correct password", status: "pending",
			password: password, wantErr: auth.ErrAccountPending,
		},
		{
			// A wrong password learns nothing: the rejection is generic whatever
			// the account's state, so login stays enumeration-proof.
			name: "pending with wrong password", status: "pending",
			password: "wrong", wantErr: auth.ErrInvalidCredentials,
		},
		{
			// A disabled account stays hidden even to its password holder.
			name: "disabled with correct password", status: "disabled",
			password: password, wantErr: auth.ErrInvalidCredentials,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &userDirectoryQuerier{
				queryRow: func(string, ...any) pgx.Row {
					return userDirectoryRowWithStatus(t, 42, code, "alice@example.com", password, tc.status)
				},
			}
			_, err := NewUserRepoDirectory(q).VerifyCredentials(context.Background(), "alice@example.com", tc.password)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifyCredentials err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestUserRepoDirectoryVerifyCredentialsNormalizesEmail(t *testing.T) {
	const password = "hunter2"

	code, err := id.ParsePublicCode("0123456789")
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}

	var gotLookup string
	q := &userDirectoryQuerier{
		queryRow: func(sql string, args ...any) pgx.Row {
			if !strings.Contains(sql, "WHERE mail_address = $1") {
				t.Errorf("lookup SQL = %q", sql)
			}
			gotLookup = args[0].(string)
			return activeUserDirectoryRow(t, 42, code, "alice@example.com", password)
		},
	}

	got, err := NewUserRepoDirectory(q).VerifyCredentials(context.Background(), "  Alice@Example.COM\t", password)
	if err != nil {
		t.Fatalf("VerifyCredentials: %v", err)
	}
	if gotLookup != "alice@example.com" {
		t.Fatalf("lookup email = %q, want alice@example.com", gotLookup)
	}
	if got.UserID != mustUserID(t, "42") {
		t.Fatalf("UserID = %v, want 42", got.UserID)
	}
	if got.PublicCode != code {
		t.Fatalf("PublicCode = %q, want %q", got.PublicCode, code)
	}
}

func TestUserRepoDirectoryVerifyCredentialsRejectsMalformedEmailWithoutLookup(t *testing.T) {
	q := &userDirectoryQuerier{
		queryRow: func(sql string, args ...any) pgx.Row {
			t.Fatalf("QueryRow called for malformed email: sql=%q args=%v", sql, args)
			return userDirectoryRow{}
		},
	}

	_, err := NewUserRepoDirectory(q).VerifyCredentials(context.Background(), "Alice <alice@example.com>", "hunter2")
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("VerifyCredentials err = %v, want ErrInvalidCredentials", err)
	}
}
