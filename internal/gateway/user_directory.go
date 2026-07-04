package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/userrepo"
)

// dummyHashPassword is lazily hashed once to seed the timing-equalizer. Its
// value is irrelevant — only the cost of comparing against it matters.
const dummyHashPassword = "blabby-timing-equalizer"

var dummyPasswordHash = sync.OnceValue(func() []byte {
	hash, err := auth.HashPassword(dummyHashPassword)
	if err != nil {
		panic(fmt.Sprintf("gateway: precompute dummy password hash: %v", err))
	}
	return hash
})

// authDBTimeout bounds a single account lookup. It is owned here (the callee), per
// the auth.Authenticator contract that an IO-bound implementation enforces its own
// deadline: token validation runs on the WebSocket auth path with a background
// context, so a stalled database must not block the connection actor.
const authDBTimeout = 3 * time.Second

// UserRepoDirectory is the gateway's seam over userrepo for authentication: it
// verifies login credentials and resolves the U… token subject to an internal
// UserID. One type satisfies both auth.CredentialVerifier and
// auth.PublicCodeResolver — the gateway builds it once and passes it as both,
// the way the retired in-memory store backed both lookup and resolution.
type UserRepoDirectory struct {
	repo      *userrepo.Repo
	pool      postgres.Querier
	dummyHash []byte
}

var (
	_ auth.CredentialVerifier = (*UserRepoDirectory)(nil)
	_ auth.PublicCodeResolver = (*UserRepoDirectory)(nil)
)

// NewUserRepoDirectory builds a UserRepoDirectory over pool. It owns a
// userrepo.Repo with a nil id source: login and token validation read accounts
// but never mint them. It precomputes a dummy bcrypt hash so a login for an
// unknown email spends the same time as one for a known email with a wrong
// password, denying a timing oracle for account enumeration.
func NewUserRepoDirectory(pool postgres.Querier) *UserRepoDirectory {
	return &UserRepoDirectory{repo: userrepo.New(nil), pool: pool, dummyHash: dummyPasswordHash()}
}

// VerifyCredentials looks up the account by normalized email and checks the
// password under the stored bcrypt scheme. It returns auth.ErrInvalidCredentials
// for every rejection (unknown email, wrong password, non-active account) so the
// cases are indistinguishable, and a wrapped error for an infrastructure failure.
// On a successful login whose stored hash is below the target cost it re-hashes
// synchronously, best-effort.
func (d *UserRepoDirectory) VerifyCredentials(ctx context.Context, mailAddress, password string) (auth.VerifiedUser, error) {
	ctx, cancel := context.WithTimeout(ctx, authDBTimeout)
	defer cancel()

	addr, err := domain.NewMailAddress(mailAddress)
	if err != nil {
		// A malformed address can match no account. Reject as invalid credentials
		// (not a distinct error) so login never reveals whether an address is
		// well-formed-but-unknown vs. malformed; spend the dummy-hash time too.
		_ = auth.VerifyPassword(d.dummyHash, password)
		return auth.VerifiedUser{}, auth.ErrInvalidCredentials
	}

	user, err := d.repo.FindByEmail(ctx, d.pool, addr)
	if errors.Is(err, userrepo.ErrUserNotFound) {
		// Compare against the dummy hash so a missing account is not faster to
		// reject than a wrong password. The result is intentionally discarded.
		_ = auth.VerifyPassword(d.dummyHash, password)
		return auth.VerifiedUser{}, auth.ErrInvalidCredentials
	}
	if err != nil {
		return auth.VerifiedUser{}, fmt.Errorf("gateway: verify credentials: %w", err)
	}

	if err := auth.VerifyPassword(user.PasswordHash, password); err != nil {
		return auth.VerifiedUser{}, auth.ErrInvalidCredentials
	}
	if user.Status == domain.UserStatusPending {
		// The password verified above, so the caller proved account ownership:
		// revealing the pending state is not an enumeration oracle, and it lets
		// the client route the user to email verification.
		return auth.VerifiedUser{}, auth.ErrAccountPending
	}
	if user.Status != domain.UserStatusActive {
		// A disabled account stays a generic rejection even to its password
		// holder.
		return auth.VerifiedUser{}, auth.ErrInvalidCredentials
	}

	if auth.PasswordNeedsRehash(user.PasswordHash) {
		d.rehashPassword(ctx, user.ID, password)
	}
	return auth.VerifiedUser{UserID: user.ID, PublicCode: user.PublicCode}, nil
}

// ResolveUserID maps a U… public_code (the JWT subject) to its internal UserID.
// An unknown code becomes auth.ErrPublicCodeUnknown so ValidateToken treats the
// token as invalid; any other failure is a backend error, logged here and returned
// so the auth layer can answer 503 rather than 401.
func (d *UserRepoDirectory) ResolveUserID(ctx context.Context, code id.PublicCode) (id.UserID, error) {
	ctx, cancel := context.WithTimeout(ctx, authDBTimeout)
	defer cancel()

	userID, err := d.repo.ResolveByPublicCode(ctx, d.pool, code)
	if errors.Is(err, userrepo.ErrUserNotFound) {
		return id.UserID{}, auth.ErrPublicCodeUnknown
	}
	if err != nil {
		slog.Error("resolve user public_code failed", "error", err)
		return id.UserID{}, err
	}
	return userID, nil
}

// rehashPassword re-stores the credential at the target cost after a successful
// login. It is best-effort: the user already authenticated, so a failed write is
// logged but does not fail the login.
func (d *UserRepoDirectory) rehashPassword(ctx context.Context, userID id.UserID, password string) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		slog.Error("password rehash: hash failed", "user_id", userID, "error", err)
		return
	}
	if err := d.repo.SetPasswordHash(ctx, d.pool, userID, hash); err != nil {
		slog.Warn("password rehash: store failed", "user_id", userID, "error", err)
	}
}
