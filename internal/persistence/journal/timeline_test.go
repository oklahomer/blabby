package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
)

func mustMessageQuery(t *testing.T, raw string) domain.MessageQuery {
	t.Helper()
	q, err := domain.NewMessageQuery(raw)
	if err != nil {
		t.Fatalf("NewMessageQuery(%q): %v", raw, err)
	}
	return q
}

// timelineRow builds one row in the timeline scan order:
// id, type, text, occurred_at, public_code, display_name.
func timelineRow(eid int64, kind, text string, code, name string) []any {
	return []any{eid, kind, text, time.Unix(0, 0).UTC(), code, name}
}

func TestTimeline_FirstPageInterleavesKinds(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &fakeRows{rows: [][]any{
			timelineRow(102, "message_posted", "hello 世界", "A000000001", "alice"),
			timelineRow(101, "member_joined", "", "B000000002", "bob"),
		}}, nil
	}}

	entries, hasMore, err := New(nil).Timeline(context.Background(), fq, mustRoomID(t, 4), TimelineParams{Limit: 2})
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if hasMore {
		t.Error("hasMore = true, want false when the look-ahead row is absent")
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	msg, joined := entries[0], entries[1]
	if msg.ID.Int64() != 102 || msg.Kind != EntryMessage || msg.Text != "hello 世界" ||
		msg.User.Code.String() != "A000000001" || msg.User.Name != "alice" {
		t.Errorf("message entry = %+v", msg)
	}
	if joined.ID.Int64() != 101 || joined.Kind != EntryMemberJoined || joined.Text != "" ||
		joined.User.Name != "bob" {
		t.Errorf("member entry = %+v", joined)
	}
	if !strings.Contains(gotSQL, "JOIN service_user") {
		t.Errorf("SQL is missing the author join: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "ORDER BY e.id DESC") {
		t.Errorf("SQL is missing the newest-first keyset ordering: %s", gotSQL)
	}
	if strings.Contains(gotSQL, "e.id <") || strings.Contains(gotSQL, "&@~") {
		t.Errorf("SQL carries cursor/search clauses for zero params: %s", gotSQL)
	}
	// room id plus the Limit+1 look-ahead are the only arguments.
	if len(gotArgs) != 2 || gotArgs[0].(int64) != 4 || gotArgs[1].(int) != 3 {
		t.Errorf("args = %v, want [4 3]", gotArgs)
	}
}

func TestTimeline_LookAheadRowSetsHasMore(t *testing.T) {
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{
			timelineRow(103, "message_posted", "three", "A000000001", "alice"),
			timelineRow(102, "message_posted", "two", "A000000001", "alice"),
			timelineRow(101, "message_posted", "one", "A000000001", "alice"),
		}}, nil
	}}

	entries, hasMore, err := New(nil).Timeline(context.Background(), fq, mustRoomID(t, 4), TimelineParams{Limit: 2})
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(entries) != 2 || entries[1].ID.Int64() != 102 {
		t.Fatalf("entries = %+v, want exactly the first 2", entries)
	}
	if !hasMore {
		t.Error("hasMore = false, want true when the look-ahead row is present")
	}
}

func TestTimeline_CursorAndSearchClauses(t *testing.T) {
	var gotSQL string
	var gotArgs []any
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		gotSQL, gotArgs = sql, args
		return &fakeRows{}, nil
	}}

	before, err := id.ParseEventID("900")
	if err != nil {
		t.Fatalf("ParseEventID: %v", err)
	}
	_, _, err = New(nil).Timeline(context.Background(), fq, mustRoomID(t, 4), TimelineParams{
		Query:  mustMessageQuery(t, `cats OR "dogs\`),
		Before: before,
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if !strings.Contains(gotSQL, "e.id < $2") {
		t.Errorf("SQL is missing the keyset cursor: %s", gotSQL)
	}
	// The search filter applies to messages only and matches literally: each
	// whitespace-separated term becomes a quoted Groonga phrase, so operator
	// words and query syntax in the fragment cannot change semantics.
	if !strings.Contains(gotSQL, "e.type = 'message_posted'") || !strings.Contains(gotSQL, "&@~ $3") {
		t.Errorf("SQL is missing the message search clauses: %s", gotSQL)
	}
	if !strings.Contains(gotSQL, "LIMIT $4") {
		t.Errorf("SQL is missing the limit: %s", gotSQL)
	}
	wantQuery := `"cats" "OR" "\"dogs\\"`
	if len(gotArgs) != 4 || gotArgs[0].(int64) != 4 || gotArgs[1].(int64) != 900 ||
		gotArgs[2].(string) != wantQuery || gotArgs[3].(int) != 6 {
		t.Errorf("args = %v, want [4 900 %s 6]", gotArgs, wantQuery)
	}
}

func TestTimeline_UnknownKindFails(t *testing.T) {
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{timelineRow(101, "bogus", "", "A000000001", "alice")}}, nil
	}}
	if _, _, err := New(nil).Timeline(context.Background(), fq, mustRoomID(t, 4), TimelineParams{Limit: 5}); err == nil {
		t.Fatal("Timeline: want an error for an unknown event type")
	}
}

func TestTimeline_MalformedUserCodeFails(t *testing.T) {
	fq := &fakeQuerier{query: func(sql string, args ...any) (pgx.Rows, error) {
		return &fakeRows{rows: [][]any{timelineRow(101, "message_posted", "hi", "not-a-code", "alice")}}, nil
	}}
	if _, _, err := New(nil).Timeline(context.Background(), fq, mustRoomID(t, 4), TimelineParams{Limit: 5}); err == nil {
		t.Fatal("Timeline: want an error for a malformed user public_code")
	}
}

func TestTimeline_PropagatesQueryError(t *testing.T) {
	boom := errors.New("connection reset")
	fq := &fakeQuerier{query: func(string, ...any) (pgx.Rows, error) { return nil, boom }}
	if _, _, err := New(nil).Timeline(context.Background(), fq, mustRoomID(t, 4), TimelineParams{Limit: 5}); !errors.Is(err, boom) {
		t.Fatalf("Timeline err = %v, want wrapped %v", err, boom)
	}
}
