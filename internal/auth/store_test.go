package auth_test

import (
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
	"golang.org/x/crypto/bcrypt"
)

func TestInMemoryUserStore_Lookup(t *testing.T) {
	store := auth.NewInMemoryUserStore()

	tests := []struct {
		name     string
		username string
		wantErr  bool
		wantID   string
	}{
		{
			name:     "alice is found",
			username: "alice",
			wantErr:  false,
			wantID:   auth.UserIDAlice.String(),
		},
		{
			name:     "bob is found",
			username: "bob",
			wantErr:  false,
			wantID:   auth.UserIDBob.String(),
		},
		{
			name:     "charlie is found",
			username: "charlie",
			wantErr:  false,
			wantID:   auth.UserIDCharlie.String(),
		},
		{
			name:     "unknown user returns error",
			username: "unknown",
			wantErr:  true,
		},
		{
			name:     "empty username returns error",
			username: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := store.Lookup(tt.username)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if user.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", user.ID, tt.wantID)
			}
			if user.Username != tt.username {
				t.Errorf("Username = %q, want %q", user.Username, tt.username)
			}
		})
	}
}

func TestInMemoryUserStore_PasswordValidation(t *testing.T) {
	store := auth.NewInMemoryUserStore()

	tests := []struct {
		name     string
		username string
		password string
		wantOK   bool
	}{
		{
			name:     "valid password for alice matches",
			username: "alice",
			password: "alice123",
			wantOK:   true,
		},
		{
			name:     "valid password for bob matches",
			username: "bob",
			password: "bob123",
			wantOK:   true,
		},
		{
			name:     "invalid password does not match",
			username: "alice",
			password: "wrong",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, err := store.Lookup(tt.username)
			if err != nil {
				t.Fatalf("lookup failed: %v", err)
			}

			err = bcrypt.CompareHashAndPassword(user.PasswordHash, []byte(tt.password))
			if tt.wantOK && err != nil {
				t.Errorf("expected password match, got error: %v", err)
			}
			if !tt.wantOK && err == nil {
				t.Error("expected password mismatch, got nil error")
			}
		})
	}
}
