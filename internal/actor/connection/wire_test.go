package connection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEncodeAuthOk(t *testing.T) {
	got := encodeAuthOk()
	want := []byte(`{"type":"auth_ok"}`)
	if !bytes.Equal(got, want) {
		t.Errorf("encodeAuthOk = %s, want %s", got, want)
	}
}

func TestEncodeAuthError(t *testing.T) {
	tests := []struct {
		name    string
		code    int32
		status  string
		message string
		want    string
	}{
		{
			name:    "invalid token",
			code:    1001,
			status:  "AUTH_INVALID_TOKEN",
			message: "invalid token",
			want:    `{"code":1001,"message":"invalid token","status":"AUTH_INVALID_TOKEN","type":"auth_error"}`,
		},
		{
			name:    "expired token",
			code:    1002,
			status:  "AUTH_EXPIRED_TOKEN",
			message: "token has expired",
			want:    `{"code":1002,"message":"token has expired","status":"AUTH_EXPIRED_TOKEN","type":"auth_error"}`,
		},
		{
			name:    "missing token",
			code:    1003,
			status:  "AUTH_MISSING_TOKEN",
			message: "missing authentication token",
			want:    `{"code":1003,"message":"missing authentication token","status":"AUTH_MISSING_TOKEN","type":"auth_error"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeAuthError(tc.code, tc.status, tc.message)
			if string(got) != tc.want {
				t.Errorf("encodeAuthError = %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestEncodeMessage(t *testing.T) {
	got := encodeMessage("general", "alice", "hello world", 1700000000000)
	want := `{"room_id":"general","sender_id":"alice","text":"hello world","timestamp":1700000000000,"type":"message"}`
	if string(got) != want {
		t.Errorf("encodeMessage = %s\nwant %s", got, want)
	}
}

func TestEncodeJoined(t *testing.T) {
	got := encodeJoined("general", "alice")
	want := `{"room_id":"general","type":"joined","user_id":"alice"}`
	if string(got) != want {
		t.Errorf("encodeJoined = %s\nwant %s", got, want)
	}
}

func TestEncodeLeft(t *testing.T) {
	got := encodeLeft("general", "alice")
	want := `{"room_id":"general","type":"left","user_id":"alice"}`
	if string(got) != want {
		t.Errorf("encodeLeft = %s\nwant %s", got, want)
	}
}

// Non-auth-error builders must not include a "code" key.
func TestNonAuthErrorBuildersOmitCode(t *testing.T) {
	cases := map[string][]byte{
		"auth_ok": encodeAuthOk(),
		"message": encodeMessage("r", "s", "t", 1),
		"joined":  encodeJoined("r", "u"),
		"left":    encodeLeft("r", "u"),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("%s: unmarshal: %v", name, err)
			}
			if _, ok := m["code"]; ok {
				t.Errorf("%s: must not include \"code\" key, got %s", name, out)
			}
		})
	}
}

// Sensitive substrings (token text, error strings, internal paths) must
// never appear in any builder output regardless of inputs.
func TestEncodersNeverLeakSensitiveSubstrings(t *testing.T) {
	const tokenSubstr = "ey-secret-jwt-payload"
	const internalErr = "panic: runtime error"
	out1 := encodeMessage("r", "s", "harmless body", 1)
	out2 := encodeAuthError(1001, "AUTH_INVALID_TOKEN", "invalid token")

	for _, b := range [][]byte{out1, out2} {
		s := string(b)
		if strings.Contains(s, tokenSubstr) {
			t.Errorf("output leaked token substring: %s", s)
		}
		if strings.Contains(s, internalErr) {
			t.Errorf("output leaked internal error: %s", s)
		}
	}
}

func TestDecodeInboundFrame_Auth(t *testing.T) {
	got := decodeInboundFrame([]byte(`{"type":"auth","token":"abc.def.ghi"}`))
	a, ok := got.(*InboundAuth)
	if !ok {
		t.Fatalf("got %T, want *InboundAuth", got)
	}
	if a.Token.String() != "abc.def.ghi" {
		t.Errorf("Token = %q, want %q", a.Token.String(), "abc.def.ghi")
	}
}

func TestDecodeInboundFrame_AuthMissingToken(t *testing.T) {
	got := decodeInboundFrame([]byte(`{"type":"auth"}`))
	v, ok := got.(*ProtocolViolation)
	if !ok {
		t.Fatalf("got %T, want *ProtocolViolation", got)
	}
	if v.Reason != protocolViolationMissingToken {
		t.Errorf("Reason = %q, want %q", v.Reason, protocolViolationMissingToken)
	}
}

func TestDecodeInboundFrame_AuthMalformedTokenField(t *testing.T) {
	got := decodeInboundFrame([]byte(`{"type":"auth","token":123}`))
	d, ok := got.(*DecodeFailed)
	if !ok {
		t.Fatalf("got %T, want *DecodeFailed", got)
	}
	if d.Reason != decodeFailureMalformedJSON {
		t.Errorf("Reason = %q, want %q", d.Reason, decodeFailureMalformedJSON)
	}
}

func TestDecodeInboundFrame_Pong(t *testing.T) {
	got := decodeInboundFrame([]byte(`{"type":"pong"}`))
	if _, ok := got.(*AppPongReceived); !ok {
		t.Fatalf("got %T, want *AppPongReceived", got)
	}
}

func TestDecodeInboundFrame_UnknownType(t *testing.T) {
	got := decodeInboundFrame([]byte(`{"type":"banana"}`))
	d, ok := got.(*DecodeFailed)
	if !ok {
		t.Fatalf("got %T, want *DecodeFailed", got)
	}
	if d.Reason != decodeFailureUnknownType {
		t.Errorf("Reason = %q, want %q", d.Reason, decodeFailureUnknownType)
	}
}

func TestDecodeInboundFrame_MalformedJSON(t *testing.T) {
	got := decodeInboundFrame([]byte(`not-json`))
	d, ok := got.(*DecodeFailed)
	if !ok {
		t.Fatalf("got %T, want *DecodeFailed", got)
	}
	if d.Reason != decodeFailureMalformedJSON {
		t.Errorf("Reason = %q, want %q", d.Reason, decodeFailureMalformedJSON)
	}
}
