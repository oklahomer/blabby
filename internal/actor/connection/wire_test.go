package connection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/oklahomer/blabby/internal/errcode"
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
		code    errcode.Code
		message string
		want    string
	}{
		{
			name:    "invalid token",
			code:    errcode.AuthInvalidToken,
			message: "invalid token",
			want:    `{"code":1001,"message":"invalid token","status":"AUTH_INVALID_TOKEN","type":"auth_error"}`,
		},
		{
			name:    "expired token",
			code:    errcode.AuthExpiredToken,
			message: "token has expired",
			want:    `{"code":1002,"message":"token has expired","status":"AUTH_EXPIRED_TOKEN","type":"auth_error"}`,
		},
		{
			name:    "missing token",
			code:    errcode.AuthMissingToken,
			message: "missing authentication token",
			want:    `{"code":1003,"message":"missing authentication token","status":"AUTH_MISSING_TOKEN","type":"auth_error"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeAuthError(tc.code, tc.message)
			if string(got) != tc.want {
				t.Errorf("encodeAuthError = %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestEncodeOutboundErrorResponse(t *testing.T) {
	frame, ok := encodeOutboundMessage(&ErrorResponse{
		Code:    errcode.RoomNotMember,
		Message: "not a member",
	})
	if !ok {
		t.Fatal("encodeOutboundMessage returned ok=false")
	}
	if frame.messageType != websocket.TextMessage {
		t.Errorf("messageType = %d, want %d", frame.messageType, websocket.TextMessage)
	}
	if frame.eventKind != "error" {
		t.Errorf("eventKind = %q, want error", frame.eventKind)
	}
	want := `{"code":2001,"message":"not a member","status":"ROOM_NOT_MEMBER","type":"error"}`
	if string(frame.data) != want {
		t.Errorf("encoded data = %s\nwant %s", frame.data, want)
	}
}

func TestEncodeMessage(t *testing.T) {
	got := encodeMessage("general", UserRef{ID: "UA000000001", Name: "Alice Liddell"}, "hello world", 1700000000000, "987654321")
	want := `{"event_id":"987654321","room_id":"general","sender":{"id":"UA000000001","name":"Alice Liddell"},"text":"hello world","timestamp":1700000000000,"type":"message"}`
	if string(got) != want {
		t.Errorf("encodeMessage = %s\nwant %s", got, want)
	}
}

func TestEncodeJoined(t *testing.T) {
	got := encodeMember("joined", "general", UserRef{ID: "UA000000001", Name: "Alice Liddell"}, "987654321", 1700000000000)
	want := `{"event_id":"987654321","room_id":"general","timestamp":1700000000000,"type":"joined","user":{"id":"UA000000001","name":"Alice Liddell"}}`
	if string(got) != want {
		t.Errorf("encodeMember(joined) = %s\nwant %s", got, want)
	}
}

func TestEncodeLeft(t *testing.T) {
	got := encodeMember("left", "general", UserRef{ID: "UA000000001", Name: "Alice Liddell"}, "987654322", 1700000000000)
	want := `{"event_id":"987654322","room_id":"general","timestamp":1700000000000,"type":"left","user":{"id":"UA000000001","name":"Alice Liddell"}}`
	if string(got) != want {
		t.Errorf("encodeMember(left) = %s\nwant %s", got, want)
	}
}

// Non-auth-error builders must not include a "code" key.
func TestNonAuthErrorBuildersOmitCode(t *testing.T) {
	cases := map[string][]byte{
		"auth_ok": encodeAuthOk(),
		"message": encodeMessage("r", UserRef{ID: "s", Name: "sn"}, "t", 1, "1"),
		"joined":  encodeMember("joined", "r", UserRef{ID: "u", Name: "un"}, "1", 1),
		"left":    encodeMember("left", "r", UserRef{ID: "u", Name: "un"}, "1", 1),
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
	out1 := encodeMessage("r", UserRef{ID: "s", Name: "sn"}, "harmless body", 1, "1")
	out2 := encodeAuthError(errcode.AuthInvalidToken, "invalid token")

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
