package persistence

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// verificationValues builds one row in the scanVerification column order.
func verificationValues(uid int64, hash []byte, expiresAt time.Time, attempts, resendCount int, lastSentAt *time.Time, createdAt time.Time) []any {
	return []any{uid, hash, expiresAt, attempts, resendCount, lastSentAt, createdAt}
}

func TestVerificationCreate_Success(t *testing.T) {
	expiry := time.Unix(1000, 0).UTC()
	sentAt := time.Unix(500, 0).UTC()
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		gotSQL, gotArgs = sql, args
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	}}

	err := NewVerificationRepo().Create(context.Background(), fq, VerificationCreateParams{
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

func TestVerificationCreate_PropagatesError(t *testing.T) {
	sentinel := errors.New("pk collision")
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.CommandTag{}, sentinel
	}}
	err := NewVerificationRepo().Create(context.Background(), fq, VerificationCreateParams{UserID: mustUserID(t, 7)})
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

	v, err := NewVerificationRepo().FindByUser(context.Background(), fq, mustUserID(t, 7))
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
	v, err := NewVerificationRepo().FindByUser(context.Background(), fq, mustUserID(t, 7))
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
	if _, err := NewVerificationRepo().FindByUser(context.Background(), fq, mustUserID(t, 9)); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("FindByUser: got %v, want ErrVerificationNotFound", err)
	}
}

func TestIncrementAttempts_Success(t *testing.T) {
	var gotSQL string
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL = sql
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{3}) }}
	}}
	got, err := NewVerificationRepo().IncrementAttempts(context.Background(), fq, mustUserID(t, 7))
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
	if _, err := NewVerificationRepo().IncrementAttempts(context.Background(), fq, mustUserID(t, 9)); !errors.Is(err, ErrVerificationNotFound) {
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
	err := NewVerificationRepo().Resend(context.Background(), fq, VerificationResendParams{
		UserID: mustUserID(t, 7), PinHash: []byte("$2a$new"), ExpiresAt: time.Unix(3, 0).UTC(), SentAt: time.Unix(2, 0).UTC(),
	}, VerificationResendPolicy{PreviousSentBefore: time.Unix(1, 0).UTC(), MaxResendCount: 5})
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
	if err := NewVerificationRepo().Resend(context.Background(), fq, VerificationResendParams{UserID: mustUserID(t, 9)}, VerificationResendPolicy{}); !errors.Is(err, ErrVerificationNotFound) {
		t.Fatalf("Resend: got %v, want ErrVerificationNotFound", err)
	}
}

func TestResend_RateLimited(t *testing.T) {
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(dest ...any) error { return assignAll(dest, []any{false, true}) }}
	}}
	if err := NewVerificationRepo().Resend(context.Background(), fq, VerificationResendParams{UserID: mustUserID(t, 7)}, VerificationResendPolicy{}); !errors.Is(err, ErrVerificationRateLimited) {
		t.Fatalf("Resend: got %v, want ErrVerificationRateLimited", err)
	}
}

func TestResend_PropagatesError(t *testing.T) {
	sentinel := errors.New("db down")
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		return fakeRow{scan: func(...any) error { return sentinel }}
	}}
	if err := NewVerificationRepo().Resend(context.Background(), fq, VerificationResendParams{UserID: mustUserID(t, 7)}, VerificationResendPolicy{}); !errors.Is(err, sentinel) {
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
	if err := NewVerificationRepo().Delete(context.Background(), fq, mustUserID(t, 7)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	fq := &fakeQuerier{exec: func(string, ...any) (pgconn.CommandTag, error) {
		return pgconn.NewCommandTag("DELETE 0"), nil
	}}
	if err := NewVerificationRepo().Delete(context.Background(), fq, mustUserID(t, 9)); !errors.Is(err, ErrVerificationNotFound) {
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
