package auth

import (
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/oklahomer/blabby/internal/id"
)

// UserStore defines the contract for looking up user credentials.
type UserStore interface {
	// Lookup returns the stored user for the given username.
	Lookup(username string) (*StoredUser, error)
}

// StoredUser represents a user record with hashed credentials.
type StoredUser struct {
	ID           string
	Username     string
	PasswordHash []byte
}

// InMemoryUserStore is a hardcoded user store for development and testing.
type InMemoryUserStore struct {
	users map[string]StoredUser // keyed by username, for credential lookup
	byID  map[string]StoredUser // keyed by user ID, for reverse profile lookup
}

// Pre-generated UUIDs for the hardcoded test users.
// Fixed values keep tests deterministic.
var (
	UserIDAlice   = uuid.MustParse("019644a2-b78c-7e10-8b1a-ee7c502a0001")
	UserIDBob     = uuid.MustParse("019644a2-b78c-7e10-8b1a-ee7c502a0002")
	UserIDCharlie = uuid.MustParse("019644a2-b78c-7e10-8b1a-ee7c502a0003")
)

// NewInMemoryUserStore creates a store pre-configured with test users.
func NewInMemoryUserStore() *InMemoryUserStore {
	users := []struct {
		id       uuid.UUID
		username string
		password string
	}{
		{id: UserIDAlice, username: "alice", password: "alice123"},
		{id: UserIDBob, username: "bob", password: "bob123"},
		{id: UserIDCharlie, username: "charlie", password: "charlie123"},
	}

	store := &InMemoryUserStore{
		users: make(map[string]StoredUser, len(users)),
		byID:  make(map[string]StoredUser, len(users)),
	}

	for _, u := range users {
		hash, err := bcrypt.GenerateFromPassword([]byte(u.password), bcrypt.DefaultCost)
		if err != nil {
			panic(fmt.Sprintf("failed to hash password for %s: %v", u.username, err))
		}
		stored := StoredUser{
			ID:           u.id.String(),
			Username:     u.username,
			PasswordHash: hash,
		}
		store.users[u.username] = stored
		store.byID[stored.ID] = stored
	}

	return store
}

// Lookup returns the stored user matching the username, or an error if not found.
func (s *InMemoryUserStore) Lookup(username string) (*StoredUser, error) {
	user, ok := s.users[username]
	if !ok {
		return nil, fmt.Errorf("user not found: %s", username)
	}
	return &user, nil
}

// Resolve returns the profile (id + display name) for a user ID. It is the
// reverse of credential lookup: the User grain calls it on activation to seed
// its UserRef. Returns an error if no user has that ID.
func (s *InMemoryUserStore) Resolve(userID id.UserID) (id.UserRef, error) {
	user, ok := s.byID[userID.String()]
	if !ok {
		return id.UserRef{}, fmt.Errorf("user not found: %s", userID)
	}
	return id.NewUserRef(userID, user.Username)
}
