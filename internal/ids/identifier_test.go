package ids

import (
	"errors"
	"strings"
	"testing"
)

// TestParseIdentifier covers every structural rule the shared parser
// enforces. Because both UserID and RoomID dispatch into the same
// parser, exhaustive coverage here keeps the per-type tests focused on
// the wrap shape.
func TestParseIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{name: "simple lowercase slug", input: "general", want: "general"},
		{name: "uuid string", input: "550e8400-e29b-41d4-a716-446655440000", want: "550e8400-e29b-41d4-a716-446655440000"},
		{name: "trims leading whitespace", input: "  alice", want: "alice"},
		{name: "trims trailing whitespace", input: "alice\t\n", want: "alice"},
		{name: "trims both sides", input: " \talice\n", want: "alice"},
		{name: "preserves internal hyphens and digits", input: "room-42", want: "room-42"},
		{name: "preserves underscores", input: "user_42", want: "user_42"},
		{name: "max length boundary", input: strings.Repeat("a", MaxIdentifierBytes), want: strings.Repeat("a", MaxIdentifierBytes)},

		{name: "rejects empty input", input: "", wantErr: ErrEmptyIdentifier},
		{name: "rejects whitespace-only input", input: "   ", wantErr: ErrEmptyIdentifier},
		{name: "rejects tab-only input", input: "\t\t", wantErr: ErrEmptyIdentifier},
		{name: "rejects newline-only input", input: "\n", wantErr: ErrEmptyIdentifier},

		{name: "rejects over-length input", input: strings.Repeat("a", MaxIdentifierBytes+1), wantErr: ErrIdentifierTooLong},

		{name: "rejects NUL byte", input: "alice\x00", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects DEL byte", input: "alice\x7f", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects low control byte", input: "alice\x01", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects internal space", input: "alice bob", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects internal tab", input: "alice\tbob", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects ideographic space", input: "alice　bob", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects slash", input: "foo/bar", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects leading slash", input: "/foo", wantErr: ErrIdentifierInvalidChar},
		{name: "rejects trailing slash", input: "foo/", wantErr: ErrIdentifierInvalidChar},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIdentifier(tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("parseIdentifier(%q) err = %v, want errors.Is %v", tt.input, err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("parseIdentifier(%q) returned non-empty %q on failure", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIdentifier(%q) returned unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("parseIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}