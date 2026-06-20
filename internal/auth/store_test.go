package auth_test

import (
	"testing"

	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/id"
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

func TestInMemoryUserStore_Resolve(t *testing.T) {
	store := auth.NewInMemoryUserStore()

	tests := []struct {
		name     string
		rawID    string
		wantErr  bool
		wantName string
	}{
		{name: "alice resolves to her username", rawID: auth.UserIDAlice.String(), wantName: "alice"},
		{name: "bob resolves to his username", rawID: auth.UserIDBob.String(), wantName: "bob"},
		{name: "charlie resolves to his username", rawID: auth.UserIDCharlie.String(), wantName: "charlie"},
		{name: "unknown id returns error", rawID: "999", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, err := id.ParseUserID(tt.rawID)
			if err != nil {
				t.Fatalf("ParseUserID(%q): %v", tt.rawID, err)
			}
			ref, err := store.Resolve(uid)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.ID() != uid {
				t.Errorf("ID() = %v, want %v", ref.ID(), uid)
			}
			if ref.Name() != tt.wantName {
				t.Errorf("Name() = %q, want %q", ref.Name(), tt.wantName)
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
