package domain_test

import (
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

func mustUserID(t *testing.T, raw int64) id.UserID {
	t.Helper()
	u, err := id.NewUserID(raw)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", raw, err)
	}
	return u
}

func TestNewUserRef(t *testing.T) {
	valid := mustUserID(t, 1)
	code := mustPublicCode(t, "A000000001")

	tests := []struct {
		name     string
		userID   id.UserID
		code     id.PublicCode
		display  string
		wantErr  bool
		wantName string // expected Name() on success (post-trim)
	}{
		{name: "valid", userID: valid, code: code, display: "Alice", wantName: "Alice"},
		{name: "name is trimmed", userID: valid, code: code, display: "  Alice  ", wantName: "Alice"},
		{name: "empty name rejected", userID: valid, code: code, display: "", wantErr: true},
		{name: "whitespace-only name rejected", userID: valid, code: code, display: "   ", wantErr: true},
		{name: "over-long name rejected", userID: valid, code: code, display: strings.Repeat("x", 257), wantErr: true},
		{name: "zero-value id rejected", userID: id.UserID{}, code: code, display: "Alice", wantErr: true},
		// The public code is load-bearing: it is the only user identity that
		// crosses to a client, so a ref cannot exist without one.
		{name: "zero-value public code rejected", userID: valid, code: id.PublicCode{}, display: "Alice", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := domain.NewUserRef(tc.userID, tc.code, tc.display)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got ref %+v", ref)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ref.ID() != tc.userID {
				t.Errorf("ID() = %v, want %v", ref.ID(), tc.userID)
			}
			if ref.PublicCode() != tc.code {
				t.Errorf("PublicCode() = %v, want %v", ref.PublicCode(), tc.code)
			}
			if ref.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", ref.Name(), tc.wantName)
			}
		})
	}
}

func TestUserRefPublicID(t *testing.T) {
	ref, err := domain.NewUserRef(mustUserID(t, 1), mustPublicCode(t, "A000000001"), "Alice")
	if err != nil {
		t.Fatalf("NewUserRef: %v", err)
	}
	if got := ref.PublicID(); got != "UA000000001" {
		t.Errorf("PublicID() = %q, want the U… client code", got)
	}
}
