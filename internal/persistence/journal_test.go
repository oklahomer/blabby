package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestAppendMembership(t *testing.T) {
	cases := []struct {
		name      string
		kind      MemberEventKind
		wantType  string
		eventName string
	}{
		{"joined", MemberJoined, "member_joined", "alice"},
		{"left", MemberLeft, "member_left", "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			occurred := time.Unix(1700000000, 0).UTC()
			var gotSQL string
			var gotArgs []any
			fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
				gotSQL, gotArgs = sql, args
				return fakeRow{scan: func(dest ...any) error {
					*(dest[0].(*time.Time)) = occurred
					return nil
				}}
			}}

			eventID, ts, err := NewJournal(stubIDSource{id: 9001}).AppendMembership(
				context.Background(), fq, mustRoomID(t, 4), mustUserRef(t, 1, "alice"), tc.kind)
			if err != nil {
				t.Fatalf("AppendMembership: %v", err)
			}
			if eventID.Int64() != 9001 {
				t.Errorf("event id = %d, want 9001 (minted)", eventID.Int64())
			}
			if !ts.Equal(occurred) {
				t.Errorf("occurred_at = %v, want %v (server clock)", ts, occurred)
			}
			// args: id, room_id, type, user_id, payload.
			if gotArgs[0].(int64) != 9001 || gotArgs[1].(int64) != 4 ||
				gotArgs[2].(string) != tc.wantType || gotArgs[3].(int64) != 1 {
				t.Errorf("args = %v, want [9001 4 %s 1 <payload>]", gotArgs, tc.wantType)
			}
			var payload memberEventPayload
			if err := json.Unmarshal(gotArgs[4].([]byte), &payload); err != nil {
				t.Fatalf("payload unmarshal: %v", err)
			}
			if payload.DisplayName != "alice" {
				t.Errorf("payload.display_name = %q, want alice", payload.DisplayName)
			}
			// occurred_at is the server clock, not deduplicated (no client_key).
			if !strings.Contains(gotSQL, "now()") || strings.Contains(gotSQL, "client_key") {
				t.Errorf("unexpected append SQL: %s", gotSQL)
			}
			if !strings.Contains(gotSQL, "$3::event_type") {
				t.Errorf("append SQL must cast the type param to the enum: %s", gotSQL)
			}
		})
	}
}

func TestAppendMessage(t *testing.T) {
	occurred := time.Unix(1700000000, 0).UTC()
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{queryRow: func(sql string, args ...any) pgx.Row {
		gotSQL, gotArgs = sql, args
		return fakeRow{scan: func(dest ...any) error {
			*(dest[0].(*time.Time)) = occurred
			return nil
		}}
	}}

	eventID, ts, err := NewJournal(stubIDSource{id: 9002}).AppendMessage(
		context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 1), "hello 世界")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if eventID.Int64() != 9002 {
		t.Errorf("event id = %d, want 9002 (minted)", eventID.Int64())
	}
	if !ts.Equal(occurred) {
		t.Errorf("occurred_at = %v, want %v (server clock)", ts, occurred)
	}
	// args: id, room_id, user_id, payload — the type is a literal in the SQL.
	if gotArgs[0].(int64) != 9002 || gotArgs[1].(int64) != 4 || gotArgs[2].(int64) != 1 {
		t.Errorf("args = %v, want [9002 4 1 <payload>]", gotArgs)
	}
	var payload messagePayload
	if err := json.Unmarshal(gotArgs[3].([]byte), &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if payload.Text != "hello 世界" {
		t.Errorf("payload.text = %q, want the message text", payload.Text)
	}
	// occurred_at is the server clock; client_key stays null until send
	// idempotency is wired.
	if !strings.Contains(gotSQL, "'message_posted'") || !strings.Contains(gotSQL, "now()") ||
		strings.Contains(gotSQL, "client_key") {
		t.Errorf("unexpected append SQL: %s", gotSQL)
	}
}

func TestAppendMessage_MintErrorSkipsDB(t *testing.T) {
	sentinel := errors.New("lease expired")
	called := false
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		called = true
		return fakeRow{scan: func(...any) error { return nil }}
	}}
	_, _, err := NewJournal(stubIDSource{err: sentinel}).AppendMessage(
		context.Background(), fq, mustRoomID(t, 4), mustUserID(t, 1), "hello")
	if !errors.Is(err, sentinel) {
		t.Fatalf("AppendMessage: got %v, want the mint error", err)
	}
	if called {
		t.Error("must not touch the DB when minting fails")
	}
}

func TestAppendMembership_UnknownKindSkipsDB(t *testing.T) {
	called := false
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		called = true
		return fakeRow{scan: func(...any) error { return nil }}
	}}
	_, _, err := NewJournal(stubIDSource{id: 1}).AppendMembership(
		context.Background(), fq, mustRoomID(t, 4), mustUserRef(t, 1, "alice"), MemberEventKind(0))
	if err == nil {
		t.Fatal("AppendMembership: want an error for an unknown kind")
	}
	if called {
		t.Error("must not touch the DB (or mint) for an unknown kind")
	}
}

func TestAppendMembership_MintErrorSkipsDB(t *testing.T) {
	sentinel := errors.New("lease expired")
	called := false
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		called = true
		return fakeRow{scan: func(...any) error { return nil }}
	}}
	_, _, err := NewJournal(stubIDSource{err: sentinel}).AppendMembership(
		context.Background(), fq, mustRoomID(t, 4), mustUserRef(t, 1, "alice"), MemberJoined)
	if !errors.Is(err, sentinel) {
		t.Fatalf("AppendMembership: got %v, want the mint error", err)
	}
	if called {
		t.Error("must not touch the DB when minting fails")
	}
}
