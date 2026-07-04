package journal

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TestJournalIntegration exercises AppendMembership against a real database.
// Skipped unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance
// (e.g. `make up`) with the schema + dev seed applied (room 4, user 1). It mints a
// unique id per run and deletes the row it created, so it is re-runnable.
func TestJournalIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BLABBY_DATABASE_URL"))
	if dsn == "" {
		t.Skip("BLABBY_DATABASE_URL not set; skipping database integration test")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, postgres.Config{
		DSN: dsn, MaxConns: 4, MaxConnIdleTime: time.Minute, MaxConnLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	// Registered as the first cleanup so it runs after (LIFO) the row-deleting
	// cleanups below; a defer would close the pool before t.Cleanup callbacks
	// fire, turning every delete into a silent no-op.
	t.Cleanup(pool.Close)

	// A time-based id avoids colliding with seed rows or a prior run.
	rawID := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM event WHERE id = $1", rawID)
	})

	j := New(stubIDSource{id: rawID})
	eventID, occurredAt, err := j.AppendMembership(ctx, pool, mustRoomID(t, 4), mustUserRef(t, 1, "alice"), MemberJoined)
	if err != nil {
		t.Fatalf("AppendMembership: %v", err)
	}
	if eventID.Int64() != rawID {
		t.Fatalf("event id = %d, want %d", eventID.Int64(), rawID)
	}
	if occurredAt.IsZero() {
		t.Fatal("occurred_at is zero, want the server clock")
	}

	// The row is persisted with the member_joined type and the actor's name.
	var (
		gotType    string
		gotUserID  int64
		gotPayload string
	)
	err = pool.QueryRow(ctx,
		"SELECT type::text, user_id, payload->>'display_name' FROM event WHERE id = $1", rawID,
	).Scan(&gotType, &gotUserID, &gotPayload)
	if err != nil {
		t.Fatalf("read back event: %v", err)
	}
	if gotType != "member_joined" || gotUserID != 1 || gotPayload != "alice" {
		t.Errorf("event row = {type:%q user_id:%d display_name:%q}, want {member_joined 1 alice}", gotType, gotUserID, gotPayload)
	}

	// AppendMessage persists a message_posted row whose payload carries the text
	// under the key the PGroonga search index covers, with a null client_key.
	msgID := rawID + 1
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM event WHERE id = $1", msgID)
	})
	msgEventID, msgOccurredAt, err := New(stubIDSource{id: msgID}).AppendMessage(
		ctx, pool, mustRoomID(t, 4), mustUserID(t, 1), "hello 世界")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if msgEventID.Int64() != msgID {
		t.Fatalf("message event id = %d, want %d", msgEventID.Int64(), msgID)
	}
	if msgOccurredAt.IsZero() {
		t.Fatal("message occurred_at is zero, want the server clock")
	}
	var (
		gotMsgType string
		gotText    string
		gotKeyNull bool
	)
	err = pool.QueryRow(ctx,
		"SELECT type::text, payload->>'text', client_key IS NULL FROM event WHERE id = $1", msgID,
	).Scan(&gotMsgType, &gotText, &gotKeyNull)
	if err != nil {
		t.Fatalf("read back message event: %v", err)
	}
	if gotMsgType != "message_posted" || gotText != "hello 世界" || !gotKeyNull {
		t.Errorf("message row = {type:%q text:%q client_key_null:%t}, want {message_posted, the text, true}",
			gotMsgType, gotText, gotKeyNull)
	}
}

// TestTimelineIntegration exercises Timeline against a real database: an
// isolated room is seeded with an interleaved membership + message history,
// then paged, cursored, and searched through the live PGroonga index. Skipped
// unless BLABBY_DATABASE_URL points at a reachable PostgreSQL instance with the
// schema + dev seed applied (user 1 authors every event).
func TestTimelineIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("BLABBY_DATABASE_URL"))
	if dsn == "" {
		t.Skip("BLABBY_DATABASE_URL not set; skipping database integration test")
	}
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, postgres.Config{
		DSN: dsn, MaxConns: 4, MaxConnIdleTime: time.Minute, MaxConnLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	// An isolated room keeps other tests' (and seed) events out of the
	// assertions. Events delete before the room so the FK never blocks cleanup.
	base := time.Now().UnixNano()
	roomID := mustRoomID(t, base)
	code, err := id.NewPublicCode()
	if err != nil {
		t.Fatalf("NewPublicCode: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM event WHERE room_id = $1", base)
		_, _ = pool.Exec(context.Background(), "DELETE FROM room WHERE id = $1", base)
	})
	if _, err := pool.Exec(ctx,
		"INSERT INTO room (id, public_code, display_name, created_by, status) VALUES ($1, $2, 'Timeline Room', 1, 'active')",
		base, code.String()); err != nil {
		t.Fatalf("seed room: %v", err)
	}

	// History, oldest first: joined, three messages (one CJK, one with Groonga
	// syntax), left. Ids ascend so the timeline order is deterministic.
	alice := mustUserRef(t, 1, "alice")
	if _, _, err := New(stubIDSource{id: base + 1}).AppendMembership(ctx, pool, roomID, alice, MemberJoined); err != nil {
		t.Fatalf("AppendMembership(joined): %v", err)
	}
	messages := []struct {
		eid  int64
		text string
	}{
		{base + 2, "morning 世界 report"},
		{base + 3, "morning standup notes"},
		{base + 4, `50% off OR "quoted" deal`},
	}
	for _, m := range messages {
		if _, _, err := New(stubIDSource{id: m.eid}).AppendMessage(ctx, pool, roomID, mustUserID(t, 1), m.text); err != nil {
			t.Fatalf("AppendMessage(%q): %v", m.text, err)
		}
	}
	if _, _, err := New(stubIDSource{id: base + 5}).AppendMembership(ctx, pool, roomID, alice, MemberLeft); err != nil {
		t.Fatalf("AppendMembership(left): %v", err)
	}

	j := New(nil)
	entryIDs := func(entries []Entry) []int64 {
		out := make([]int64, len(entries))
		for i, e := range entries {
			out[i] = e.ID.Int64()
		}
		return out
	}

	// Full page: all five events newest-first, kinds interleaved, authors
	// joined from service_user.
	all, hasMore, err := j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 10})
	if err != nil {
		t.Fatalf("Timeline(all): %v", err)
	}
	if hasMore || len(all) != 5 || all[0].ID.Int64() != base+5 || all[4].ID.Int64() != base+1 {
		t.Fatalf("Timeline(all) = %v (hasMore=%t), want [%d..%d] newest-first", entryIDs(all), hasMore, base+5, base+1)
	}
	if all[0].Kind != EntryMemberLeft || all[1].Kind != EntryMessage || all[4].Kind != EntryMemberJoined {
		t.Errorf("kinds = %v/%v/%v, want left/message/joined at the edges", all[0].Kind, all[1].Kind, all[4].Kind)
	}
	if all[1].Author.Name != "alice" || all[1].Author.Code.FormatUser() != "UA000000001" {
		t.Errorf("author = %+v, want alice / UA000000001 (joined from service_user)", all[1].Author)
	}

	// Keyset paging: two per page, cursored by the last id of the prior page.
	page1, hasMore, err := j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 2})
	if err != nil {
		t.Fatalf("Timeline(page 1): %v", err)
	}
	if !hasMore || len(page1) != 2 || page1[0].ID.Int64() != base+5 || page1[1].ID.Int64() != base+4 {
		t.Fatalf("page 1 = %v (hasMore=%t), want [%d %d] with more", entryIDs(page1), hasMore, base+5, base+4)
	}
	page2, hasMore, err := j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 2, Before: page1[1].ID})
	if err != nil {
		t.Fatalf("Timeline(page 2): %v", err)
	}
	if !hasMore || len(page2) != 2 || page2[0].ID.Int64() != base+3 || page2[1].ID.Int64() != base+2 {
		t.Fatalf("page 2 = %v (hasMore=%t), want [%d %d] with more", entryIDs(page2), hasMore, base+3, base+2)
	}
	page3, hasMore, err := j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 2, Before: page2[1].ID})
	if err != nil {
		t.Fatalf("Timeline(page 3): %v", err)
	}
	if hasMore || len(page3) != 1 || page3[0].ID.Int64() != base+1 {
		t.Fatalf("page 3 = %v (hasMore=%t), want just [%d]", entryIDs(page3), hasMore, base+1)
	}

	mustQuery := func(raw string) domain.MessageQuery {
		t.Helper()
		q, err := domain.NewMessageQuery(raw)
		if err != nil {
			t.Fatalf("NewMessageQuery(%q): %v", raw, err)
		}
		return q
	}

	// Search matches messages only: "morning" hits two messages and never the
	// membership entries, newest-first.
	found, hasMore, err := j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 10, Query: mustQuery("morning")})
	if err != nil {
		t.Fatalf("Timeline(q=morning): %v", err)
	}
	if hasMore || len(found) != 2 || found[0].ID.Int64() != base+3 || found[1].ID.Int64() != base+2 {
		t.Fatalf("q=morning = %v (hasMore=%t), want [%d %d]", entryIDs(found), hasMore, base+3, base+2)
	}

	// CJK full-text: a two-character fragment finds the CJK message.
	found, _, err = j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 10, Query: mustQuery("世界")})
	if err != nil {
		t.Fatalf("Timeline(q=世界): %v", err)
	}
	if len(found) != 1 || found[0].ID.Int64() != base+2 {
		t.Fatalf("q=世界 = %v, want [%d]", entryIDs(found), base+2)
	}

	// Multi-term queries AND together, and operator-looking/syntax-carrying
	// fragments match literally: OR is a term (not an operator), quotes are
	// text. Each finds only the message actually containing every term.
	found, _, err = j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 10, Query: mustQuery("morning 世界")})
	if err != nil {
		t.Fatalf("Timeline(q=morning 世界): %v", err)
	}
	if len(found) != 1 || found[0].ID.Int64() != base+2 {
		t.Fatalf("q=morning 世界 = %v, want [%d] (terms AND together)", entryIDs(found), base+2)
	}
	found, _, err = j.Timeline(ctx, pool, roomID, TimelineParams{Limit: 10, Query: mustQuery(`OR "quoted"`)})
	if err != nil {
		t.Fatalf(`Timeline(q=OR "quoted"): %v`, err)
	}
	if len(found) != 1 || found[0].ID.Int64() != base+4 {
		t.Fatalf(`q=OR "quoted" = %v, want [%d] (matched literally)`, entryIDs(found), base+4)
	}
}
