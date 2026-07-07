// The email_verification table holds the per-account email-verification challenge —
// a bcrypt-hashed PIN, its expiry, the failed-attempt count that locks the
// challenge, and the resend bookkeeping (last_sent_at / resend_count) that bounds
// how often a fresh PIN can be issued. Rows are parsed into a typed value object at
// the boundary (parse, don't validate). It is hash-agnostic: callers pass an
// already-hashed PIN as bytes, so the hashing scheme lives with the verification
// domain, not here.

package persistence

import (
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/id"
)

// Verification is the domain view of an email_verification row. LastSentAt is the
// zero time only if the column was NULL (never sent); the repo always sets it on
// Create/Resend, so in practice it is populated.
type Verification struct {
	UserID      id.UserID
	PinHash     []byte
	ExpiresAt   time.Time
	Attempts    int
	ResendCount int
	LastSentAt  time.Time
	CreatedAt   time.Time
}

// Expired reports whether the challenge's PIN has lapsed at time now. An expired
// challenge must be rejected even if the submitted PIN matches.
func (v Verification) Expired(now time.Time) bool { return !now.Before(v.ExpiresAt) }

// verificationRow is the raw row scanned from Postgres: primitive columns in the
// fixed order the repo's SELECT/RETURNING lists them. last_sent_at is nullable, so
// it scans through a pointer. toDomain parses it into a Verification.
type verificationRow struct {
	userID      int64
	pinHash     []byte
	expiresAt   time.Time
	attempts    int
	resendCount int
	lastSentAt  *time.Time
	createdAt   time.Time
}

// toDomain parses a raw row into a Verification, enforcing the id invariant at the
// boundary. A non-positive user_id is a data-integrity error surfaced to the
// caller rather than silently trusted.
func (vr verificationRow) toDomain() (Verification, error) {
	userID, err := id.NewUserID(vr.userID)
	if err != nil {
		return Verification{}, fmt.Errorf("persistence: row user_id: %w", err)
	}
	var lastSentAt time.Time
	if vr.lastSentAt != nil {
		lastSentAt = *vr.lastSentAt
	}
	return Verification{
		UserID:      userID,
		PinHash:     vr.pinHash,
		ExpiresAt:   vr.expiresAt,
		Attempts:    vr.attempts,
		ResendCount: vr.resendCount,
		LastSentAt:  lastSentAt,
		CreatedAt:   vr.createdAt,
	}, nil
}

// scanVerification reads one row in the fixed column order and parses it into a
// Verification. It returns the raw Scan error unwrapped (so callers can map
// pgx.ErrNoRows).
func scanVerification(s scannable) (Verification, error) {
	var vr verificationRow
	if err := s.Scan(
		&vr.userID, &vr.pinHash, &vr.expiresAt, &vr.attempts,
		&vr.resendCount, &vr.lastSentAt, &vr.createdAt,
	); err != nil {
		return Verification{}, err
	}
	return vr.toDomain()
}
