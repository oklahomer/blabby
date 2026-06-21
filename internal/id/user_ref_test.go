package id_test

import (
	"strings"
	"testing"

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

	tests := []struct {
		name     string
		userID   id.UserID
		display  string
		wantErr  bool
		wantName string // expected Name() on success (post-trim)
	}{
		{name: "valid", userID: valid, display: "Alice", wantName: "Alice"},
		{name: "name is trimmed", userID: valid, display: "  Alice  ", wantName: "Alice"},
		{name: "empty name rejected", userID: valid, display: "", wantErr: true},
		{name: "whitespace-only name rejected", userID: valid, display: "   ", wantErr: true},
		{name: "over-long name rejected", userID: valid, display: strings.Repeat("x", 257), wantErr: true},
		{name: "zero-value id rejected", userID: id.UserID{}, display: "Alice", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := id.NewUserRef(tc.userID, tc.display)
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
			if ref.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", ref.Name(), tc.wantName)
			}
		})
	}
}
