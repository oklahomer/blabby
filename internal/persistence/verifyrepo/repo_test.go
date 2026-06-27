package verifyrepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier for exercising the repo's control
// flow without a database. queryRow drives FindByUser and IncrementAttempts; exec
// drives Create, Resend, and Delete.
type fakeQuerier struct {
	queryRow func(sql string, args ...any) pgx.Row
	exec     func(sql string, args ...any) (pgconn.CommandTag, error)
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(sql, args...)
}

func (f *fakeQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	return nil, fmt.Errorf("unexpected Query: %s", sql)
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(sql, args...)
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

// assignAll copies column values into the Scan destinations, matching the types
// scanVerification passes (incl. **time.Time for the nullable last_sent_at) plus
// the bare *int IncrementAttempts scans.
func assignAll(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("fake scan: %d destinations, %d values", len(dest), len(values))
	}
	for i := range dest {
		switch d := dest[i].(type) {
		case *int64:
			*d = values[i].(int64)
		case *int:
			*d = values[i].(int)
		case *bool:
			*d = values[i].(bool)
		case *[]byte:
			*d = values[i].([]byte)
		case *time.Time:
			*d = values[i].(time.Time)
		case **time.Time:
			*d = values[i].(*time.Time)
		default:
			return fmt.Errorf("fake scan: unsupported destination %T", dest[i])
		}
	}
	return nil
}

// verificationValues builds one row in the scanVerification column order.
func verificationValues(uid int64, hash []byte, expiresAt time.Time, attempts, resendCount int, lastSentAt *time.Time, createdAt time.Time) []any {
	return []any{uid, hash, expiresAt, attempts, resendCount, lastSentAt, createdAt}
}

func mustUserID(t *testing.T, v int64) id.UserID {
	t.Helper()
	uid, err := id.NewUserID(v)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", v, err)
	}
	return uid
}

func TestCreate_Success(t *testing.T) {
	expiry := time.Unix(1000, 0).UTC()
	sentAt := time.Unix(500, 0).UTC()
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		gotSQL, gotArgs = sql, args
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}}

	err := New().Create(context.Background(), fq, CreateParams{
		UserID: mustUserID(t, 7), PinHash: []byte("$2a$hash"), ExpiresAt: expiry, SentAt: sentAt,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if gotArgs[0].(int64) != 7 || string(gotArgs[1].([]byte)) != "$2a$hash" {
		t.Errorf("create args = %v", gotArgs)
	}
	if !gotArgs[2].(time.Time).Equal(expiry) || !gotArgs[3].(time.Time).Equal(sentAt) {
		t.Errorf("create timestamp args = %v / %v", gotArgs[2], gotArgs[3])
	}
	if !strings.Contains(gotSQL, "INSERT INTO email_verification") {
		t.Errorf("unexpected insert SQL: %s", gotSQL)
	}
}

func TestCreate_PropagatesError(t *testing.T) {
	sentinel := errors.New("pk collision")
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, sentinel
	}}
	err := New().Create(context.Background(), fq, CreateParams{UserID: mustUserID(t, 7)})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Create: got %v, want the db error", err)
	}
}

func TestFindByUser_Success(t *testing.T) {
	expiry := time.Unix(2000, 0).UTC()
	sent := time.Unix(1900, 0).UTC()
	created := time.Unix(1000, 0).UTC()
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		if args[0].(int64) != 7 {
			t.Errorf("lookup arg = %v, want 7", args[0])
		}
		return fakeRow{scan: func(dest ...any) error {
			return assignAll(dest, verificationValues(7, []byte("$2a$hash"), expiry, 2, 1, &sent, created))
		}}
	}}

	v, err := New().FindByUser(context.Background(), fq, mustUserID(t, 7))
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}
	if v.UserID.Int64() != 7 || v.Attempts != 2 || v.ResendCount != 1 {
		t.Errorf("verification = %+v", v)
	}
	if !v.ExpiresAt.Equal(expiry) || !v.LastSentAt.Equal(sent) {
		t.Errorf("timestamps = %v / %v", v.ExpiresAt, v.LastSentAt)
	}
}

func TestFindByUser_NullLastSentAt(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(dest ...any) error {
			return assignAll(dest, verificationValues(7, []byte("h"), time.Unix(1, 0).UTC(), 0, 0, nil, time.Unix(1, 0).UTC()))
		}}
	}}
	v, err := New().FindByUser(context.Background(), fq, mustUserID(t, 7))
	if err != nil {
		t.Fatalf("FindByUser: %v", err)
	}
	if !v.LastSentAt.IsZero() {
		t.Errorf("LastSentAt = %v, want zero for NULL", v.LastSentAt)
	}
}

func TestFindByUser_NotFound(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}
	if _, err := New().FindByUser(context.Background(), fq, mustUserID(t, 9)); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("FindByUser: got %v, want ErrVerificationNotFound", err)
	}
}

func TestIncrementAttempts_Success(t *testing.T) {
	var gotSQL string
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL = sql
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{3}) }}
	}}
	got, err := New().IncrementAttempts(context.Background(), fq, mustUserID(t, 7))
	if err != nil {
		t.Fatalf("IncrementAttempts: %v", err)
	}
	if got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
	if !strings.Contains(gotSQL, "attempts = attempts + 1") || !strings.Contains(gotSQL, "RETURNING attempts") {
		t.Errorf("unexpected update SQL: %s", gotSQL)
	}
}

func TestIncrementAttempts_NotFound(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return pgx.ErrNoRows }}
	}}
	if _, err := New().IncrementAttempts(context.Background(), fq, mustUserID(t, 9)); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("IncrementAttempts: got %v, want ErrVerificationNotFound", err)
	}
}

func TestResend_Success(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL, gotArgs = sql, args
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{true, true}) }}
	}}
	err := New().Resend(context.Background(), fq, ResendParams{
		UserID: mustUserID(t, 7), PinHash: []byte("$2a$new"), ExpiresAt: time.Unix(3, 0).UTC(), SentAt: time.Unix(2, 0).UTC(),
	}, ResendPolicy{PreviousSentBefore: time.Unix(1, 0).UTC(), MaxResendCount: 5})
	if err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if gotArgs[0].(int64) != 7 || string(gotArgs[1].([]byte)) != "$2a$new" {
		t.Errorf("resend args = %v", gotArgs)
	}
	if !gotArgs[4].(time.Time).Equal(time.Unix(1, 0).UTC()) || gotArgs[5].(int) != 5 {
		t.Errorf("resend policy args = %v", gotArgs[4:])
	}
	if !strings.Contains(gotSQL, "UPDATE email_verification") {
		t.Errorf("unexpected resend SQL: %s", gotSQL)
	}
}

func TestResend_NotFound(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{false, false}) }}
	}}
	if err := New().Resend(context.Background(), fq, ResendParams{UserID: mustUserID(t, 9)}, ResendPolicy{}); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("Resend: got %v, want ErrVerificationNotFound", err)
	}
}

func TestResend_RateLimited(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{false, true}) }}
	}}
	if err := New().Resend(context.Background(), fq, ResendParams{UserID: mustUserID(t, 7)}, ResendPolicy{}); !errors.Is(err, ErrVerificationRateLimited) {
		t.Fatalf("Resend: got %v, want ErrVerificationRateLimited", err)
	}
}

func TestResend_PropagatesError(t *testing.T) {
	sentinel := errors.New("db down")
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return sentinel }}
	}}
	if err := New().Resend(context.Background(), fq, ResendParams{UserID: mustUserID(t, 7)}, ResendPolicy{}); !errors.Is(err, sentinel) {
		t.Fatalf("Resend: got %v, want db error", err)
	}
}

func TestDelete_Success(t *testing.T) {
	fq := &fakeQuerier{exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		if args[0].(int64) != 7 || !strings.Contains(sql, "DELETE FROM email_verification") {
			t.Errorf("delete sql/args = %s / %v", sql, args)
		}
		return pgconn.NewCommandTag("DELETE 1"), nil
	}}
	if err := New().Delete(context.Background(), fq, mustUserID(t, 7)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 0"), nil
	}}
	if err := New().Delete(context.Background(), fq, mustUserID(t, 9)); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("Delete: got %v, want ErrVerificationNotFound", err)
	}
}

func TestExpired(t *testing.T) {
	expiry := time.Unix(1000, 0).UTC()
	v := Verification{ExpiresAt: expiry}
	if v.Expired(expiry.Add(-time.Second)) {
		t.Error("Expired before expiry = true, want false")
	}
	if !v.Expired(expiry) {
		t.Error("Expired at expiry = false, want true (expiry is exclusive)")
	}
	if !v.Expired(expiry.Add(time.Second)) {
		t.Error("Expired after expiry = false, want true")
	}
}
