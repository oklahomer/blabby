package api

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// mintToken builds a syntactically-valid JWT-shaped string with the
// given payload bytes. Signature segment is irrelevant — DecodeSub
// does not verify.
func mintToken(t *testing.T, payload []byte) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := base64.RawURLEncoding.EncodeToString([]byte("not-a-real-signature"))
	return header + "." + body + "." + sig
}

func TestDecodeSub(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		token   string
		wantSub string
		wantErr bool
	}{
		{
			name:    "valid token with sub claim",
			token:   mintToken(t, []byte(`{"sub":"u-rina","iat":1700000000}`)),
			wantSub: "u-rina",
		},
		{
			name:    "valid token with uuid sub",
			token:   mintToken(t, []byte(`{"sub":"550e8400-e29b-41d4-a716-446655440000"}`)),
			wantSub: "550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:    "empty token rejected",
			token:   "",
			wantErr: true,
		},
		{
			name:    "two-segment token rejected",
			token:   "header.body",
			wantErr: true,
		},
		{
			name:    "non-base64 payload rejected",
			token:   "header.!!!not-base64!!!.sig",
			wantErr: true,
		},
		{
			name:    "non-json payload rejected",
			token:   mintToken(t, []byte("not json")),
			wantErr: true,
		},
		{
			name:    "missing sub claim rejected",
			token:   mintToken(t, []byte(`{"iat":1700000000}`)),
			wantErr: true,
		},
		{
			name:    "empty sub claim rejected",
			token:   mintToken(t, []byte(`{"sub":""}`)),
			wantErr: true,
		},
		{
			name:    "oversized sub claim rejected",
			token:   mintToken(t, []byte(`{"sub":"`+strings.Repeat("a", maxSubBytes+1)+`"}`)),
			wantErr: true,
		},
		{
			name:    "control-char sub claim rejected",
			token:   mintToken(t, []byte("{\"sub\":\"a\\u0001b\"}")),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sub, err := DecodeSub(tc.token)
			if tc.wantErr {
				if !errors.Is(err, ErrMalformedJWT) {
					t.Fatalf("expected ErrMalformedJWT, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sub != tc.wantSub {
				t.Fatalf("got sub %q, want %q", sub, tc.wantSub)
			}
		})
	}
}
