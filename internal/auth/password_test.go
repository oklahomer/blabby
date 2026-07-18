package auth

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// seedHashes are the exact password_hash values the schema seed stores for the
// dev accounts (internal/persistence/schema.sql). Verifying them here pins the
// pre-hash scheme: if HashPassword/VerifyPassword ever drift from
// bcrypt(cost 12) over base64(SHA-256(password)), the seed accounts stop being
// loginable and this test fails first.
var seedHashes = map[string]string{
	"alice123":   "$2a$12$zgAytEmdKjaOvd.r0PJUKezVSto9L4PADnVozWu8XVUSuEHf.0GXq",
	"bob123":     "$2a$12$Du8qPiZd8QDAe6VZAtw1W.UPz0fm.BFu7.NruxB9BzCcbvFoxdgaq",
	"charlie123": "$2a$12$mTavr5AjYMSn1XeAjqmJp.GReMPazdDfod26WAY4z9UuEqilFKX9q",
}

func TestVerifyPassword_SeedScheme(t *testing.T) {
	for password, hash := range seedHashes {
		if err := VerifyPassword([]byte(hash), password); err != nil {
			t.Errorf("VerifyPassword(seed %q) = %v, want nil (seed scheme parity broken)", password, err)
		}
		// A wrong password must not verify against the seed hash.
		if err := VerifyPassword([]byte(hash), password+"x"); err == nil {
			t.Errorf("VerifyPassword(seed %q, wrong password) = nil, want mismatch", password)
		}
	}
}

func TestHashPassword_RoundTrip(t *testing.T) {
	const password = "correct horse battery staple"
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := VerifyPassword(hash, password); err != nil {
		t.Errorf("VerifyPassword(round-trip) = %v, want nil", err)
	}
	if err := VerifyPassword(hash, "wrong"); err == nil {
		t.Error("VerifyPassword(wrong password) = nil, want mismatch")
	}
	cost, err := bcrypt.Cost(hash)
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost != PasswordTargetCost {
		t.Errorf("cost = %d, want %d", cost, PasswordTargetCost)
	}
}

func TestHashPassword_AcceptsLongPasswords(t *testing.T) {
	base := make([]byte, 72)
	for i := range base {
		base[i] = 'a'
	}
	one := string(base) + "one"
	two := string(base) + "two"

	// Raw bcrypt rejects an input longer than 72 bytes; pre-hashing is what lets
	// HashPassword accept one at all.
	if _, err := bcrypt.GenerateFromPassword([]byte(one), PasswordTargetCost); !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("raw bcrypt over %d bytes: err = %v, want ErrPasswordTooLong", len(one), err)
	}
	hash, err := HashPassword(one)
	if err != nil {
		t.Fatalf("HashPassword(long password): %v", err)
	}

	// The full input is significant: two long passwords differing only past byte
	// 72 hash distinctly, because bcrypt hashes a digest of the whole password,
	// not its first 72 bytes.
	if err := VerifyPassword(hash, two); err == nil {
		t.Error("VerifyPassword accepted a different password sharing the first 72 bytes")
	}
	if err := VerifyPassword(hash, one); err != nil {
		t.Errorf("VerifyPassword(same long password) = %v, want nil", err)
	}
}

func TestPassword_NFDAndNFCSpellingsShareOneHash(t *testing.T) {
	// The password is NFC-normalized — never trimmed — before hashing and
	// verification, so the decomposed spelling a macOS IME emits and the
	// precomposed spelling other platforms emit are one credential.
	nfd := "pássword-secret" // á as a + combining acute (U+0301)
	nfc := "pássword-secret"  // á precomposed (U+00E1)

	hash, err := HashPassword(nfd)
	if err != nil {
		t.Fatalf("HashPassword(nfd): %v", err)
	}
	if err := VerifyPassword(hash, nfc); err != nil {
		t.Errorf("VerifyPassword(nfc spelling) = %v, want nil", err)
	}

	// Normalization is not trimming: surrounding whitespace stays significant.
	spaced, err := HashPassword(" secret-password ")
	if err != nil {
		t.Fatalf("HashPassword(spaced): %v", err)
	}
	if err := VerifyPassword(spaced, "secret-password"); err == nil {
		t.Error("VerifyPassword(trimmed spelling) = nil, want mismatch")
	}
}

func TestValidatePasswordStrength(t *testing.T) {
	// Boundary: exactly MinPasswordLen passes, one byte short fails.
	if err := ValidatePasswordStrength(strings.Repeat("a", MinPasswordLen)); err != nil {
		t.Errorf("ValidatePasswordStrength(len %d) = %v, want nil", MinPasswordLen, err)
	}
	if err := ValidatePasswordStrength(strings.Repeat("a", MinPasswordLen-1)); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("ValidatePasswordStrength(len %d) = %v, want ErrWeakPassword", MinPasswordLen-1, err)
	}
	if err := ValidatePasswordStrength(""); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("ValidatePasswordStrength(empty) = %v, want ErrWeakPassword", err)
	}
	// Whitespace is significant and never trimmed: a spaces-only password of
	// the minimum length is a valid credential (login must accept it too).
	if err := ValidatePasswordStrength(strings.Repeat(" ", MinPasswordLen)); err != nil {
		t.Errorf("ValidatePasswordStrength(spaces only) = %v, want nil", err)
	}
	// The minimum is measured on the canonical form: five "a"+combining-acute
	// pairs are 15 raw bytes but 10 once composed, which is too short.
	if err := ValidatePasswordStrength(strings.Repeat("á", 5)); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("ValidatePasswordStrength(nfd short) = %v, want ErrWeakPassword", err)
	}
}

func TestPasswordNeedsRehash(t *testing.T) {
	below, err := bcrypt.GenerateFromPassword(prehash("x"), PasswordTargetCost-2)
	if err != nil {
		t.Fatalf("generate below-target hash: %v", err)
	}
	if !PasswordNeedsRehash(below) {
		t.Errorf("PasswordNeedsRehash(cost %d) = false, want true", PasswordTargetCost-2)
	}

	atTarget, err := HashPassword("x")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if PasswordNeedsRehash(atTarget) {
		t.Errorf("PasswordNeedsRehash(cost %d) = true, want false", PasswordTargetCost)
	}

	// An unparseable hash is a data-integrity problem, not a rehash trigger.
	if PasswordNeedsRehash([]byte("not-a-bcrypt-hash")) {
		t.Error("PasswordNeedsRehash(garbage) = true, want false")
	}
}
