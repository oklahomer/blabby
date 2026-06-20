package id

import (
	"errors"
	"strings"
	"testing"
)

func TestCrockfordSymbolIsUnbiased(t *testing.T) {
	counts := make(map[byte]int)
	for i := 0; i < 256; i++ {
		sym := crockfordSymbol(byte(i))
		if !strings.ContainsRune(crockfordAlphabet, rune(sym)) {
			t.Fatalf("byte %d -> %q is not a Crockford symbol", i, sym)
		}
		counts[sym]++
	}
	if len(counts) != len(crockfordAlphabet) {
		t.Fatalf("covered %d distinct symbols, want %d", len(counts), len(crockfordAlphabet))
	}
	for sym, c := range counts {
		if c != 8 {
			t.Errorf("symbol %q produced by %d byte values, want 8 (uniform)", sym, c)
		}
	}
}

func TestNewPublicCodeIsValidAndCanonical(t *testing.T) {
	for i := 0; i < 200; i++ {
		code, err := NewPublicCode()
		if err != nil {
			t.Fatalf("NewPublicCode: %v", err)
		}
		s := code.String()
		if len(s) != publicCodeLen {
			t.Fatalf("code %q length %d, want %d", s, len(s), publicCodeLen)
		}
		for _, r := range s {
			if !strings.ContainsRune(crockfordAlphabet, r) {
				t.Fatalf("code %q contains non-Crockford %q", s, r)
			}
		}
		// A freshly generated code is already canonical, so parsing is a no-op.
		parsed, err := ParsePublicCode(s)
		if err != nil {
			t.Fatalf("ParsePublicCode(%q): %v", s, err)
		}
		if parsed.String() != s {
			t.Fatalf("re-parse changed %q -> %q", s, parsed.String())
		}
	}
}

func TestParsePublicCode(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{name: "canonical", in: "A1B2C3D4E5", want: "A1B2C3D4E5"},
		{name: "trims and uppercases", in: "  a1b2c3d4e5  ", want: "A1B2C3D4E5"},
		{name: "lenient O to 0", in: "OOOOOOOOOO", want: "0000000000"},
		{name: "lenient I and L to 1", in: "ILILILILIL", want: "1111111111"},
		{name: "too short", in: "ABC", wantErr: ErrInvalidPublicCode},
		{name: "too long", in: "ABCDEFGHJK0", wantErr: ErrInvalidPublicCode},
		{name: "invalid symbol U", in: "UUUUUUUUUU", wantErr: ErrInvalidPublicCode},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePublicCode(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParsePublicCode(%q): got %v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePublicCode(%q): %v", tc.in, err)
			}
			if got.String() != tc.want {
				t.Fatalf("ParsePublicCode(%q) = %q, want %q", tc.in, got.String(), tc.want)
			}
		})
	}
}

func TestFormatAndParsePrefixedCodes(t *testing.T) {
	code, err := NewPublicCode()
	if err != nil {
		t.Fatalf("NewPublicCode: %v", err)
	}

	t.Run("user round trip", func(t *testing.T) {
		s := code.FormatUser()
		if !strings.HasPrefix(s, "U") {
			t.Fatalf("FormatUser = %q, want a U prefix", s)
		}
		got, err := ParseUserCode(s)
		if err != nil {
			t.Fatalf("ParseUserCode(%q): %v", s, err)
		}
		if got != code {
			t.Fatalf("round trip = %q, want %q", got.String(), code.String())
		}
	})

	t.Run("room round trip", func(t *testing.T) {
		s := code.FormatRoom()
		got, err := ParseRoomCode(s)
		if err != nil {
			t.Fatalf("ParseRoomCode(%q): %v", s, err)
		}
		if got != code {
			t.Fatalf("round trip = %q, want %q", got.String(), code.String())
		}
	})

	t.Run("wrong type letter is rejected", func(t *testing.T) {
		if _, err := ParseUserCode(code.FormatRoom()); !errors.Is(err, ErrInvalidCodePrefix) {
			t.Errorf("ParseUserCode of a room code: got %v, want ErrInvalidCodePrefix", err)
		}
		if _, err := ParseRoomCode(code.FormatUser()); !errors.Is(err, ErrInvalidCodePrefix) {
			t.Errorf("ParseRoomCode of a user code: got %v, want ErrInvalidCodePrefix", err)
		}
	})

	t.Run("lowercase prefix and lenient body", func(t *testing.T) {
		got, err := ParseUserCode("u" + strings.ToLower(code.String()))
		if err != nil {
			t.Fatalf("ParseUserCode lowercase: %v", err)
		}
		if got != code {
			t.Fatalf("lenient parse = %q, want %q", got.String(), code.String())
		}
	})
}
