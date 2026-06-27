// Package verification implements blabby's email-verification challenge: the short
// numeric PIN a newly registered account confirms to become active, how it is
// hashed at rest, and how it is delivered (console by default, SMTP opt-in).
package verification

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// pinDigits is the fixed length of a verification PIN. Six digits give a
	// 1-in-10^6 chance per guess; combined with the per-challenge attempt lock and
	// short expiry, that is the guess budget — not the hash cost.
	pinDigits = 6

	// pinBound is 10^pinDigits, the exclusive upper bound of the PIN value space.
	pinBound = 1_000_000

	// pinHashCost is the bcrypt cost for a PIN at rest. The challenge expires in
	// minutes and locks after a few attempts, so the cost only has to slow an
	// attacker who exfiltrated the hash before the row was deleted; the default
	// cost is ample for a value with a 10^6 space and a short life.
	pinHashCost = bcrypt.DefaultCost
)

// ErrInvalidPIN reports that a raw value is not exactly pinDigits ASCII digits.
var ErrInvalidPIN = errors.New("verification: pin must be 6 digits")

// PIN is a verification PIN: exactly pinDigits decimal digits, held canonically
// (zero-padded). A zero-value PIN is not valid; construct one with [NewPIN] (fresh
// random) or [ParsePIN] (from client input).
type PIN struct {
	value string
}

// NewPIN generates a uniformly random PIN from crypto/rand. crypto/rand.Int draws
// an unbiased value in [0, pinBound) — no modulo skew — which is then zero-padded
// to pinDigits, so every code 000000–999999 is equally likely.
func NewPIN() (PIN, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(pinBound))
	if err != nil {
		return PIN{}, fmt.Errorf("verification: generate pin: %w", err)
	}
	return PIN{value: fmt.Sprintf("%0*d", pinDigits, n.Int64())}, nil
}

// ParsePIN validates raw client input (after trimming) as exactly pinDigits
// decimal digits and returns the canonical PIN. It does NOT consult any stored
// challenge; matching a PIN against a hash is [Verify]'s job.
func ParsePIN(raw string) (PIN, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) != pinDigits {
		return PIN{}, ErrInvalidPIN
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return PIN{}, ErrInvalidPIN
		}
	}
	return PIN{value: trimmed}, nil
}

// String returns the canonical zero-padded digits. The zero value returns "".
func (p PIN) String() string { return p.value }

// Hash returns a bcrypt hash of the PIN for storage at rest, so a database dump
// never exposes the plaintext PIN.
func (p PIN) Hash() ([]byte, error) {
	if _, err := ParsePIN(p.value); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(p.value), pinHashCost)
	if err != nil {
		return nil, fmt.Errorf("verification: hash pin: %w", err)
	}
	return hash, nil
}

// Verify reports whether raw client input matches the stored hash, in constant
// time within bcrypt. A malformed input returns [ErrInvalidPIN]; a well-formed but
// wrong PIN returns bcrypt.ErrMismatchedHashAndPassword. The verification endpoint
// collapses both — along with expiry and lockout — into one uniform response, so
// these distinct errors never reach the client.
func Verify(hash []byte, raw string) error {
	pin, err := ParsePIN(raw)
	if err != nil {
		return err
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(pin.value))
}
