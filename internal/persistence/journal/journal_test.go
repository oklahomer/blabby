package journal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// fakeQuerier is an in-memory postgres.Querier; the journal only uses QueryRow.
type fakeQuerier struct {
	queryRow func(sql string, args ...any) pgx.Row
}

var _ postgres.Querier = (*fakeQuerier)(nil)

func (f *fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("journal append does not use Exec")
}
func (f *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("journal append does not use Query")
}
func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.queryRow(sql, args...)
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

// stubIDSource mints a fixed id, or an error to drive the mint-failure path.
type stubIDSource struct {
	id  int64
	err error
}

func (s stubIDSource) Next() (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.id, nil
}

func mustUserRef(t *testing.T, rawID int64, name string) id.UserRef {
	t.Helper()
	uid, err := id.NewUserID(rawID)
	if err != nil {
		t.Fatalf("NewUserID(%d): %v", rawID, err)
	}
	ref, err := id.NewUserRef(uid, name)
	if err != nil {
		t.Fatalf("NewUserRef: %v", err)
	}
	return ref
}

func mustRoomID(t *testing.T, v int64) id.RoomID {
	t.Helper()
	rid, err := id.NewRoomID(v)
	if err != nil {
		t.Fatalf("NewRoomID(%d): %v", v, err)
	}
	return rid
}

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

			eventID, ts, err := New(stubIDSource{id: 9001}).AppendMembership(
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

func TestAppendMembership_UnknownKindSkipsDB(t *testing.T) {
	called := false
	fq := &fakeQuerier{queryRow: func(string, ...any) pgx.Row {
		called = true
		return fakeRow{scan: func(...any) error { return nil }}
	}}
	_, _, err := New(stubIDSource{id: 1}).AppendMembership(
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
	_, _, err := New(stubIDSource{err: sentinel}).AppendMembership(
		context.Background(), fq, mustRoomID(t, 4), mustUserRef(t, 1, "alice"), MemberJoined)
	if !errors.Is(err, sentinel) {
		t.Fatalf("AppendMembership: got %v, want the mint error", err)
	}
	if called {
		t.Error("must not touch the DB when minting fails")
	}
}
