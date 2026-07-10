package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testGeneration SessionGeneration = 7

func testSendMessageRequest(client *http.Client, server, roomID, text string, timeout time.Duration) SendMessageCommandRequest {
	return SendMessageCommandRequest{
		Client:     client,
		Server:     server,
		Token:      testBearerToken,
		Generation: testGeneration,
		RoomID:     roomID,
		Text:       text,
		Timeout:    timeout,
	}
}

type sendMessageTransport func(*http.Request) (*http.Response, error)

func (s sendMessageTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return s(req)
}

func sendMessageClient(body string) *http.Client {
	return &http.Client{Transport: sendMessageTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost {
			return &http.Response{
				StatusCode: http.StatusMethodNotAllowed,
				Body:       io.NopCloser(strings.NewReader(`{}`)),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
}

func TestSendMessageCmdSuccess(t *testing.T) {
	t.Parallel()
	const wantTS int64 = 1_700_000_000_000
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rooms/general/messages" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testBearerToken {
			t.Errorf("missing/incorrect bearer header: %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("expected application/json content-type, got %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		var body SendMessageRequestBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body.Text != "hello world" {
			t.Errorf("body text = %q, want %q", body.Text, "hello world")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: true, Timestamp: wantTS})
	}))
	defer srv.Close()

	msg := SendMessageCmd(testSendMessageRequest(srv.Client(), srv.URL, "general", "hello world", time.Second))()
	got, ok := msg.(SendMessageSucceeded)
	if !ok {
		t.Fatalf("expected SendMessageSucceeded, got %T: %#v", msg, msg)
	}
	if got.RoomID != "general" {
		t.Fatalf("RoomID = %q, want general", got.RoomID)
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
	if !got.At.Equal(time.UnixMilli(wantTS)) {
		t.Fatalf("At = %v, want %v", got.At, time.UnixMilli(wantTS))
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked into SendMessageSucceeded: %#v", got)
	}
}

func TestSendMessageCmdSuccessZeroTimestamp(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SendMessageResponse{Success: true, Timestamp: 0})
	}))
	defer srv.Close()

	msg := SendMessageCmd(testSendMessageRequest(srv.Client(), srv.URL, "general", "hi", time.Second))()
	got, ok := msg.(SendMessageSucceeded)
	if !ok {
		t.Fatalf("expected SendMessageSucceeded, got %T", msg)
	}
	if !got.At.IsZero() {
		t.Fatalf("expected zero At for timestamp 0, got %v", got.At)
	}
}

func TestSendMessageCmdOKWithSuccessTrueSucceeds(t *testing.T) {
	t.Parallel()
	msg := SendMessageCmd(testSendMessageRequest(
		sendMessageClient(`{"success":true,"timestamp":1}`),
		"http://example.test",
		"general",
		"hi",
		time.Second,
	))()
	got, ok := msg.(SendMessageSucceeded)
	if !ok {
		t.Fatalf("expected SendMessageSucceeded, got %T: %#v", msg, msg)
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
}

func TestSendMessageCmdOKWithFalseSuccessFails(t *testing.T) {
	t.Parallel()
	msg := SendMessageCmd(testSendMessageRequest(
		sendMessageClient(`{"success":false,"timestamp":1}`),
		"http://example.test",
		"general",
		"hi",
		time.Second,
	))()
	got, ok := msg.(SendMessageFailed)
	if !ok {
		t.Fatalf("expected SendMessageFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTPStatus = %d, want 200", got.HTTPStatus)
	}
	if got.Message != "server reported send with no success flag" {
		t.Fatalf("Message = %q", got.Message)
	}
}

func TestSendMessageCmdOKWithMissingSuccessFails(t *testing.T) {
	t.Parallel()
	msg := SendMessageCmd(testSendMessageRequest(
		sendMessageClient(`{"timestamp":1}`),
		"http://example.test",
		"general",
		"hi",
		time.Second,
	))()
	got, ok := msg.(SendMessageFailed)
	if !ok {
		t.Fatalf("expected SendMessageFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusOK {
		t.Fatalf("HTTPStatus = %d, want 200", got.HTTPStatus)
	}
	if got.Message != "server reported send with no success flag" {
		t.Fatalf("Message = %q", got.Message)
	}
}

func TestSendMessageCmdNotMember(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member",
		}})
	}))
	defer srv.Close()

	msg := SendMessageCmd(testSendMessageRequest(srv.Client(), srv.URL, "general", "hi", time.Second))()
	got, ok := msg.(SendMessageFailed)
	if !ok {
		t.Fatalf("expected SendMessageFailed, got %T", msg)
	}
	if got.Status != "ROOM_NOT_MEMBER" {
		t.Fatalf("Status = %q, want ROOM_NOT_MEMBER", got.Status)
	}
	if got.HTTPStatus != http.StatusForbidden {
		t.Fatalf("HTTPStatus = %d, want 403", got.HTTPStatus)
	}
	if got.RoomID != "general" {
		t.Fatalf("RoomID not preserved: %q", got.RoomID)
	}
	if got.Generation != testGeneration {
		t.Fatalf("Generation = %d, want %d", got.Generation, testGeneration)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestSendMessageCmdEmptyTextRejected(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 4001, Status: "INVALID_REQUEST", Message: "text is required",
		}})
	}))
	defer srv.Close()

	msg := SendMessageCmd(testSendMessageRequest(srv.Client(), srv.URL, "general", " ", time.Second))()
	got, ok := msg.(SendMessageFailed)
	if !ok {
		t.Fatalf("expected SendMessageFailed, got %T", msg)
	}
	if got.Status != "INVALID_REQUEST" || got.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("got %#v", got)
	}
}

func TestSendMessageCmdUnauthorized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 1002, Status: "AUTH_EXPIRED_TOKEN", Message: "token expired",
		}})
	}))
	defer srv.Close()

	msg := SendMessageCmd(testSendMessageRequest(srv.Client(), srv.URL, "general", "hi", time.Second))()
	got, ok := msg.(SendMessageFailed)
	if !ok {
		t.Fatalf("expected SendMessageFailed, got %T", msg)
	}
	if got.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("HTTPStatus = %d, want 401", got.HTTPStatus)
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestSendMessageCmdTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	msg := SendMessageCmd(testSendMessageRequest(&http.Client{}, addr, "general", "hi", 250*time.Millisecond))()
	got, ok := msg.(SendMessageFailed)
	if !ok {
		t.Fatalf("expected SendMessageFailed, got %T", msg)
	}
	if got.HTTPStatus != 0 {
		t.Fatalf("expected HTTPStatus 0 for transport error, got %d", got.HTTPStatus)
	}
	if got.Message == "" {
		t.Fatal("expected non-empty Message describing the transport failure")
	}
	if !rejectsToken(t, got) {
		t.Fatalf("token leaked: %#v", got)
	}
}

func TestDecodeChatMessageValid(t *testing.T) {
	t.Parallel()
	const ms int64 = 1_700_000_000_000
	raw := []byte(`{"type":"message","room_id":"general","event_id":"112233445566778899","sender":{"id":"alice","name":"Alice Liddell"},"text":"hello","timestamp":1700000000000}`)
	got, ok := DecodeChatMessage(raw)
	if !ok {
		t.Fatal("expected ok for a valid message frame")
	}
	if got.RoomID != "general" || got.Sender.ID != "alice" || got.Sender.Name != "Alice Liddell" || got.Text != "hello" {
		t.Fatalf("decoded fields wrong: %#v", got)
	}
	if got.EventID != 112233445566778899 {
		t.Fatalf("EventID = %d, want 112233445566778899", got.EventID)
	}
	if !got.At.Equal(time.UnixMilli(ms)) {
		t.Fatalf("At = %v, want %v", got.At, time.UnixMilli(ms))
	}
}

func TestDecodeChatMessageZeroTimestamp(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"message","room_id":"general","event_id":"42","sender":{"id":"alice"},"text":"hi","timestamp":0}`)
	got, ok := DecodeChatMessage(raw)
	if !ok {
		t.Fatal("expected ok")
	}
	if !got.At.IsZero() {
		t.Fatalf("expected zero At for timestamp 0, got %v", got.At)
	}
	if got.EventID != 42 {
		t.Fatalf("EventID = %d, want 42", got.EventID)
	}
}

func TestDecodeChatMessageWrongType(t *testing.T) {
	t.Parallel()
	if _, ok := DecodeChatMessage([]byte(`{"type":"joined","room_id":"general","event_id":"1","user":{"id":"alice"}}`)); ok {
		t.Fatal("expected ok=false for a non-message frame")
	}
}

func TestDecodeChatMessageMalformed(t *testing.T) {
	t.Parallel()
	if _, ok := DecodeChatMessage([]byte(`{not json`)); ok {
		t.Fatal("expected ok=false for malformed JSON")
	}
}

func TestDecodeChatMessageBadEventID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"missing":     `{"type":"message","room_id":"general","sender":{"id":"a"},"text":"hi","timestamp":1}`,
		"empty":       `{"type":"message","room_id":"general","event_id":"","sender":{"id":"a"},"text":"hi","timestamp":1}`,
		"non-numeric": `{"type":"message","room_id":"general","event_id":"abc","sender":{"id":"a"},"text":"hi","timestamp":1}`,
		"zero":        `{"type":"message","room_id":"general","event_id":"0","sender":{"id":"a"},"text":"hi","timestamp":1}`,
		"negative":    `{"type":"message","room_id":"general","event_id":"-5","sender":{"id":"a"},"text":"hi","timestamp":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := DecodeChatMessage([]byte(raw)); ok {
				t.Fatalf("expected ok=false for %s event_id", name)
			}
		})
	}
}

func TestDecodeMemberEventJoined(t *testing.T) {
	t.Parallel()
	const ms int64 = 1_700_000_000_000
	raw := []byte(`{"type":"joined","room_id":"general","event_id":"55","user":{"id":"U000000042","name":"Bob"},"timestamp":1700000000000}`)
	got, ok := DecodeMemberEvent(raw)
	if !ok {
		t.Fatal("expected ok for a valid joined frame")
	}
	if got.Kind != MemberJoined {
		t.Fatalf("Kind = %d, want MemberJoined", got.Kind)
	}
	if got.RoomID != "general" || got.User.ID != "U000000042" || got.User.Name != "Bob" {
		t.Fatalf("decoded fields wrong: %#v", got)
	}
	if got.EventID != 55 {
		t.Fatalf("EventID = %d, want 55", got.EventID)
	}
	if !got.At.Equal(time.UnixMilli(ms)) {
		t.Fatalf("At = %v, want %v", got.At, time.UnixMilli(ms))
	}
}

func TestDecodeMemberEventLeft(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"left","room_id":"general","event_id":"56","user":{"id":"U000000042","name":"Bob"},"timestamp":0}`)
	got, ok := DecodeMemberEvent(raw)
	if !ok {
		t.Fatal("expected ok for a valid left frame")
	}
	if got.Kind != MemberLeft {
		t.Fatalf("Kind = %d, want MemberLeft", got.Kind)
	}
	if !got.At.IsZero() {
		t.Fatalf("expected zero At for timestamp 0, got %v", got.At)
	}
}

func TestDecodeMemberEventRejects(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"wrong-type":   `{"type":"message","room_id":"general","event_id":"1","sender":{"id":"a"},"text":"hi","timestamp":1}`,
		"malformed":    `}{`,
		"missing-id":   `{"type":"joined","room_id":"general","user":{"id":"a"},"timestamp":1}`,
		"bad-id":       `{"type":"left","room_id":"general","event_id":"nope","user":{"id":"a"},"timestamp":1}`,
		"non-positive": `{"type":"joined","room_id":"general","event_id":"0","user":{"id":"a"},"timestamp":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := DecodeMemberEvent([]byte(raw)); ok {
				t.Fatalf("expected ok=false for %s", name)
			}
		})
	}
}

func TestDecodeErrorFrameValid(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"error","code":2001,"status":"ROOM_NOT_MEMBER","message":"not a member"}`)
	got, ok := DecodeErrorFrame(raw)
	if !ok {
		t.Fatal("expected ok for a valid error frame")
	}
	if got.Status != "ROOM_NOT_MEMBER" || got.Message != "not a member" {
		t.Fatalf("decoded fields wrong: %#v", got)
	}
}

func TestDecodeErrorFrameWrongType(t *testing.T) {
	t.Parallel()
	if _, ok := DecodeErrorFrame([]byte(`{"type":"message","text":"hi"}`)); ok {
		t.Fatal("expected ok=false for a non-error frame")
	}
}

func TestDecodeErrorFrameMalformed(t *testing.T) {
	t.Parallel()
	if _, ok := DecodeErrorFrame([]byte(`}{`)); ok {
		t.Fatal("expected ok=false for malformed JSON")
	}
}
