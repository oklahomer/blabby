package auth

import (
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
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
	users map[string]StoredUser
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
	}

	for _, u := range users {
		hash, err := bcrypt.GenerateFromPassword([]byte(u.password), bcrypt.DefaultCost)
		if err != nil {
			panic(fmt.Sprintf("failed to hash password for %s: %v", u.username, err))
		}
		store.users[u.username] = StoredUser{
			ID:           u.id.String(),
			Username:     u.username,
			PasswordHash: hash,
		}
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
