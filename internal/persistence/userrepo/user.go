// Package userrepo persists and reads the service_user table: each account's
// identity (internal Snowflake UserID plus a separate opaque public_code), its
// login email, handle, display name, password hash, and lifecycle status. It is
// the system's authority for resolving a client-facing U… code to an internal
// UserID and back, so no raw numeric user id ever crosses to the client.
//
// Like internal/persistence/roomrepo, the repo issues raw parameterized SQL — its
// statements are fixed, and a query builder would only obscure them. Rows are
// parsed into typed value objects at the boundary (parse, don't validate), so the
// rest of the package handles UserID/PublicCode/UserStatus, never bare ints or
// strings.
package userrepo

import (
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

// User is the domain view of a service_user row. Its identifiers and status are
// parsed value objects, not raw primitives; construct one only through the repo,
// which parses the row at the persistence boundary. PasswordHash is the stored
// bcrypt hash — it never leaves the auth layer, but the row carries it so the
// credential verifier can check a login without a second read.
type User struct {
	ID           id.UserID
	PublicCode   id.PublicCode
	MailAddress  domain.MailAddress
	Handle       domain.Handle
	DisplayName  string
	PasswordHash []byte
	Status       domain.UserStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PublicID renders the user's client-facing U… code.
func (u User) PublicID() string { return u.PublicCode.FormatUser() }

// userRow is the raw row scanned from Postgres: primitive columns in the fixed
// order the repo's SELECT/RETURNING lists them. toDomain parses it into a User.
type userRow struct {
	id           int64
	publicCode   string
	mailAddress  string
	handle       string
	handleNorm   string
	displayName  string
	passwordHash []byte
	status       string
	createdAt    time.Time
	updatedAt    time.Time
}

// toDomain parses a raw row into a User, enforcing row invariants at the boundary.
// A row that violates them is a data-integrity error surfaced to the caller rather
// than silently trusted.
func (ur userRow) toDomain() (User, error) {
	userID, err := id.NewUserID(ur.id)
	if err != nil {
		return User{}, fmt.Errorf("userrepo: row id: %w", err)
	}
	code, err := id.ParsePublicCode(ur.publicCode)
	if err != nil {
		return User{}, fmt.Errorf("userrepo: row public_code: %w", err)
	}
	status, err := domain.ParseUserStatus(ur.status)
	if err != nil {
		return User{}, fmt.Errorf("userrepo: row status: %w", err)
	}
	mail, err := domain.NewMailAddress(ur.mailAddress)
	if err != nil {
		return User{}, fmt.Errorf("userrepo: row mail_address: %w", err)
	}
	handle, err := domain.NewHandle(ur.handle)
	if err != nil {
		return User{}, fmt.Errorf("userrepo: row handle: %w", err)
	}
	if ur.handleNorm != handle.Normalized() {
		return User{}, fmt.Errorf("userrepo: row handle_norm: got %q, want %q", ur.handleNorm, handle.Normalized())
	}
	return User{
		ID:           userID,
		PublicCode:   code,
		MailAddress:  mail,
		Handle:       handle,
		DisplayName:  ur.displayName,
		PasswordHash: ur.passwordHash,
		Status:       status,
		CreatedAt:    ur.createdAt,
		UpdatedAt:    ur.updatedAt,
	}, nil
}

// scannable is the Scan contract shared by pgx.Row (single-row QueryRow) and
// pgx.Rows (multi-row iteration), so one scanUser helper serves both.
type scannable interface {
	Scan(dest ...any) error
}

// scanUser reads one user row in the fixed column order and parses it into a User.
// It returns the raw Scan error unwrapped (so callers can map pgx.ErrNoRows).
func scanUser(s scannable) (User, error) {
	var ur userRow
	if err := s.Scan(
		&ur.id, &ur.publicCode, &ur.mailAddress, &ur.handle, &ur.handleNorm,
		&ur.displayName, &ur.passwordHash, &ur.status, &ur.createdAt, &ur.updatedAt,
	); err != nil {
		return User{}, err
	}
	return ur.toDomain()
}
