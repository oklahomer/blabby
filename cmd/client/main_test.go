package main

import (
	"strings"
	"testing"
)

func TestParseServerURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantErr  bool
		errMatch string
	}{
		{"valid http", "http://localhost:8080", false, ""},
		{"valid https", "https://chat.example.com", false, ""},
		{"valid with path", "http://example.com:8080/api", false, ""},
		{"empty rejected", "", true, "empty"},
		{"scheme-less rejected", "localhost:8080", true, "missing scheme"},
		{"missing host rejected", "http://", true, "host"},
		{"ws scheme rejected", "ws://localhost:8080", true, "unsupported scheme"},
		{"wss scheme rejected", "wss://example.com", true, "unsupported scheme"},
		{"ftp rejected", "ftp://example.com", true, "unsupported scheme"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseServerURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				if tc.errMatch != "" && !strings.Contains(err.Error(), tc.errMatch) {
					t.Fatalf("error %q does not contain %q", err, tc.errMatch)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected non-nil URL")
			}
		})
	}
}

func TestProgramRefSendNoOpBeforeAssignment(t *testing.T) {
	ref := &programRef{}
	// Should not panic when called before set() is called.
	ref.Send("anything")
}
