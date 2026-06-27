package userrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier for exercising the repo's control
// flow without a database. queryRow drives the single-row paths (Create, the
// FindBy* lookups, ResolveByPublicCode); exec drives SetStatus.
type fakeQuerier struct {
	queryRow func(sql string, args ...any) pgx.Row
	query    func(sql string, args ...any) (pgx.Rows, error)
	exec     func(sql string, args ...any) (pgconn.CommandTag, error)
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(sql, args...)
}

func (f *fakeQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	return f.query(sql, args...)
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(sql, args...)
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

// assignAll copies column values into the Scan destinations, matching the types
// scanUser passes (*int64, *string, *[]byte, *time.Time) plus the bare *int64
// ResolveByPublicCode scans.
func assignAll(dest []any, values []any) error {
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

// userValues builds one row in the scanUser column order.
func userValues(uid int64, code, mail, handle, displayName string, hash []byte, status string) []any {
	ts := time.Unix(0, 0).UTC()
	return []any{uid, code, mail, handle, handle, displayName, hash, status, ts, ts}
}

type stubIDSource struct {
	id  int64
	err error
}

func (s *stubIDSource) Next() (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.id, nil
}

func mustUserID(t *testing.T, v int64) id.UserID {
	t.Helper()
	uid, err := id.NewUserID(v)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", v, err)
	}
	return uid
}

func createParams() CreateParams {
	return CreateParams{
		MailAddress:  "alice@example.com",
		Handle:       "Alice",
		HandleNorm:   "alice",
		DisplayName:  "Alice",
		PasswordHash: []byte("$2a$12$hash"),
		Status:       domain.UserStatusPending,
	}
}

func TestCreate_Success(t *testing.T) {
	const uid int64 = 9000001
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL, gotArgs = sql, args
		return fakeRow{scan: func(dest ...any) error {
			// RETURNING echoes the inserted row back, with the status it was given.
			return assignAll(dest, userValues(
				args[0].(int64), args[1].(string), args[2].(string), args[3].(string),
				args[5].(string), args[6].([]byte), args[7].(string)))
		}}
	}}

	user, err := New(&stubIDSource{id: uid}).Create(context.Background(), fq, createParams())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.ID.Int64() != uid {
		t.Errorf("ID = %d, want %d", user.ID.Int64(), uid)
	}
	if user.MailAddress != "alice@example.com" {
		t.Errorf("MailAddress = %q, want alice@example.com", user.MailAddress)
	}
	if user.Status != domain.UserStatusPending {
		t.Errorf("Status = %q, want pending", user.Status)
	}
	if !strings.HasPrefix(user.PublicID(), "U") {
		t.Errorf("PublicID = %q, want a U… code", user.PublicID())
	}
	if gotArgs[0].(int64) != uid || gotArgs[2].(string) != "alice@example.com" || gotArgs[7].(string) != "pending" {
		t.Errorf("insert args = %v", gotArgs)
	}
	if !strings.Contains(gotSQL, "INSERT INTO service_user") || !strings.Contains(gotSQL, "$8::user_status") {
		t.Errorf("unexpected insert SQL: %s", gotSQL)
	}
}

func TestCreate_MintErrorSkipsDB(t *testing.T) {
	sentinel := errors.New("lease expired")
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error { return nil }}
	}}

	_, err := New(&stubIDSource{err: sentinel}).Create(context.Background(), fq, createParams())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create: got %v, want the mint error", err)
	}
	if calls != 0 {
		t.Fatalf("queried %d times, want 0 (mint fails before any DB call)", calls)
	}
}

func TestCreate_ReportsPublicCodeCollision(t *testing.T) {
	// Create does not retry in place (that would break inside a caller's
	// transaction); it reports the collision so the caller re-runs the operation.
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error {
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: publicCodeConstraint}
		}}
	}}

	_, err := New(&stubIDSource{id: 7}).Create(context.Background(), fq, createParams())
	if !errors.Is(err, ErrPublicCodeCollision) {
		t.Fatalf("Create: got %v, want ErrPublicCodeCollision", err)
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1 (Create does not retry internally)", calls)
	}
}

func TestCreate_DuplicateEmailIsHardError(t *testing.T) {
	// A 23505 on a different constraint (a duplicate email, handle, or minted id)
	// is not a public_code clash: it must surface as a hard error, not a
	// recoverable collision the caller would retry with the same email.
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error {
			return &pgconn.PgError{Code: uniqueViolation, ConstraintName: "service_user_mail_address_key"}
		}}
	}}

	_, err := New(&stubIDSource{id: 7}).Create(context.Background(), fq, createParams())
	if err == nil {
		t.Fatal("Create: want an error for a duplicate email")
	}
	if errors.Is(err, ErrPublicCodeCollision) {
		t.Fatal("a duplicate-email violation must not be reported as a public_code collision")
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1", calls)
	}
}

func TestCreate_PropagatesHardError(t *testing.T) {
	sentinel := errors.New("db down")
	calls := 0
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		calls++
		return fakeRow{scan: func(...any) error { return sentinel }}
	}}

	_, err := New(&stubIDSource{id: 7}).Create(context.Background(), fq, createParams())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create: got %v, want the db error", err)
	}
	if calls != 1 {
		t.Fatalf("queried %d times, want 1 (a non-unique error is not retried)", calls)
	}
}

// findByCase drives the three single-row FindBy* lookups, which differ only in
// their WHERE clause and the argument they bind.
type findByCase struct {
	name    string
	call    func(*Repo, postgres.Querier) (User, error)
	wantArg any
}

func findByCases(t *testing.T) []findByCase {
	return []findByCase{
		{
			name: "FindByEmail",
			call: func(r *Repo, q postgres.Querier) (User, error) {
				return r.FindByEmail(context.Background(), q, "alice@example.com")
			},
			wantArg: "alice@example.com",
		},
		{
			name: "FindByHandleNorm",
			call: func(r *Repo, q postgres.Querier) (User, error) {
				return r.FindByHandleNorm(context.Background(), q, "alice")
			},
			wantArg: "alice",
		},
		{
			name: "FindByID",
			call: func(r *Repo, q postgres.Querier) (User, error) {
				return r.FindByID(context.Background(), q, mustUserID(t, 42))
			},
			wantArg: int64(42),
		},
	}
}

func TestFindBy_Success(t *testing.T) {
	for _, tc := range findByCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
				if args[0] != tc.wantArg {
					t.Errorf("lookup arg = %v, want %v", args[0], tc.wantArg)
				}
				return fakeRow{scan: func(dest ...any) error {
					return assignAll(dest, userValues(42, "A000000042", "alice@example.com", "alice", "Alice", []byte("$2a$12$hash"), "active"))
				}}
			}}

			user, err := tc.call(New(nil), fq)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if user.ID.Int64() != 42 || user.MailAddress != "alice@example.com" || user.Status != domain.UserStatusActive {
				t.Errorf("user = %+v", user)
			}
			if string(user.PasswordHash) != "$2a$12$hash" {
				t.Errorf("PasswordHash = %q, want the stored hash", user.PasswordHash)
			}
		})
	}
}

func TestFindBy_NotFound(t *testing.T) {
	for _, tc := range findByCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
				return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
			}}
			if _, err := tc.call(New(nil), fq); !errors.Is(err, ErrUserNotFound) {
				t.Fatalf("%s: got %v, want ErrUserNotFound", tc.name, err)
			}
		})
	}
}

func TestResolveByPublicCode_Success(t *testing.T) {
	code, _ := id.NewPublicCode()
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		if args[0].(string) != code.String() {
			t.Errorf("lookup arg = %q, want %q", args[0], code)
		}
		if strings.Contains(sql, "*") {
			t.Errorf("ResolveByPublicCode should select only id, got: %s", sql)
		}
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{int64(42)}) }}
	}}

	uid, err := New(nil).ResolveByPublicCode(context.Background(), fq, code)
	if err != nil {
		t.Fatalf("ResolveByPublicCode: %v", err)
	}
	if uid.Int64() != 42 {
		t.Errorf("UserID = %d, want 42", uid.Int64())
	}
}

func TestResolveByPublicCode_NotFound(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}
	code, _ := id.NewPublicCode()
	if _, err := New(nil).ResolveByPublicCode(context.Background(), fq, code); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("ResolveByPublicCode: got %v, want ErrUserNotFound", err)
	}
}

func TestSetStatus_Success(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		gotSQL, gotArgs = sql, args
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}}

	if err := New(nil).SetStatus(context.Background(), fq, mustUserID(t, 42), domain.UserStatusActive); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if gotArgs[0].(int64) != 42 || gotArgs[1].(string) != "active" {
		t.Errorf("set status args = %v", gotArgs)
	}
	if !strings.Contains(gotSQL, "UPDATE service_user") || !strings.Contains(gotSQL, "$2::user_status") {
		t.Errorf("unexpected update SQL: %s", gotSQL)
	}
}

func TestSetStatus_NotFound(t *testing.T) {
	// Zero rows affected means no account carries the id — distinct from a no-op.
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("UPDATE 0"), nil
	}}
	if err := New(nil).SetStatus(context.Background(), fq, mustUserID(t, 99), domain.UserStatusActive); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("SetStatus(missing): got %v, want ErrUserNotFound", err)
	}
}

func TestSetStatus_PropagatesError(t *testing.T) {
	sentinel := errors.New("db down")
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, sentinel
	}}
	if err := New(nil).SetStatus(context.Background(), fq, mustUserID(t, 1), domain.UserStatusActive); !errors.Is(err, sentinel) {
		t.Fatalf("SetStatus: got %v, want the db error", err)
	}
}
