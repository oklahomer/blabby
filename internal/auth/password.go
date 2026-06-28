package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// PasswordTargetCost is the bcrypt cost blabby hashes passwords at. Login
// re-hashes any credential stored below it (see PasswordNeedsRehash), so the
// effective cost ratchets up as users sign in. It matches the cost the schema
// seed used, so the seed accounts verify without a rehash.
const PasswordTargetCost = 12

// MinPasswordLen is the registration minimum password length, in bytes. The
// bcrypt(cost 12) pre-hash scheme is the real protection; this only rejects
// trivially short passwords. The maximum length is a transport concern (the
// gateway caps the request field), not a strength rule.
const MinPasswordLen = 12

// ErrWeakPassword reports a password below MinPasswordLen. Registration maps it to
// the WEAK_PASSWORD response.
var ErrWeakPassword = errors.New("auth: password is too short")

// ValidatePasswordStrength enforces the registration minimum-length policy,
// returning ErrWeakPassword for a password shorter than MinPasswordLen.
func ValidatePasswordStrength(plain string) error {
	if len(plain) < MinPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

// prehash maps an arbitrary-length password to a fixed 44-byte token by
// base64-encoding its SHA-256 digest, which is then what bcrypt actually hashes.
// bcrypt only operates on up to 72 bytes of input — golang.org/x/crypto/bcrypt
// rejects anything longer with ErrPasswordTooLong — so pre-hashing lets a
// password of any length be accepted while still contributing all of its bytes.
// SHA-256 → base64 yields a fixed 44-byte input. base64.StdEncoding (with
// padding) is load-bearing: it is the exact encoding the schema seed hashed
// under, so changing it would break login for the seed accounts.
func prehash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return []byte(base64.StdEncoding.EncodeToString(sum[:]))
}

// HashPassword returns a bcrypt hash of plain at PasswordTargetCost under the
// pre-hash scheme. It is the single place new credentials are hashed, so
// registration and login-rehash stay scheme-identical.
func HashPassword(plain string) ([]byte, error) {
	return bcrypt.GenerateFromPassword(prehash(plain), PasswordTargetCost)
}

// VerifyPassword reports whether plain matches the stored bcrypt hash under the
// pre-hash scheme. It returns nil on a match and a non-nil error otherwise
// (bcrypt.ErrMismatchedHashAndPassword for a wrong password); the comparison is
// constant-time within bcrypt.
func VerifyPassword(hash []byte, plain string) error {
	return bcrypt.CompareHashAndPassword(hash, prehash(plain))
}

// PasswordNeedsRehash reports whether a stored hash was produced below
// PasswordTargetCost and should be re-hashed on a successful login. A hash bcrypt
// cannot parse reports false: an unreadable hash is a data-integrity problem, not
// a rehash trigger, and the failed verify already rejected the login.
func PasswordNeedsRehash(hash []byte) bool {
	cost, err := bcrypt.Cost(hash)
	return err == nil && cost < PasswordTargetCost
}
