package persistence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TimelineEntryKind discriminates the timeline entry types a reader can encounter.
type TimelineEntryKind int

const (
	// EntryMessage is a posted chat message.
	EntryMessage TimelineEntryKind = iota + 1
	// EntryMemberJoined records that a user joined the room.
	EntryMemberJoined
	// EntryMemberLeft records that a user left the room.
	EntryMemberLeft
)

// parseEntryKind maps an event_type enum label onto its TimelineEntryKind, rejecting an
// unknown label so a schema/reader drift surfaces as an error, not a mislabeled
// entry.
func parseEntryKind(s string) (TimelineEntryKind, error) {
	switch s {
	case "message_posted":
		return EntryMessage, nil
	case "member_joined":
		return EntryMemberJoined, nil
	case "member_left":
		return EntryMemberLeft, nil
	default:
		return 0, fmt.Errorf("persistence: unknown event type %q", s)
	}
}

// TimelineUser is the user an event is about — the message's sender or the member who
// joined or left — as their public reference: the bare public code and the
// current display name, joined from service_user at read time. Rendering
// current names keeps the timeline consistent with the roster, and no internal
// user id leaves the read model.
type TimelineUser struct {
	Code id.PublicCode
	Name string
}

// TimelineEntry is one room-timeline event: a chat message or a membership system
// entry.
type TimelineEntry struct {
	ID         id.EventID
	Kind       TimelineEntryKind
	User       TimelineUser
	Text       string // message text; empty for membership entries
	OccurredAt time.Time
}

// TimelineParams filters and paginates Timeline. The zero values of Query and
// Before mean "no text filter" and "newest page"; Limit is the page size and
// must be positive.
type TimelineParams struct {
	Query  domain.MessageQuery
	Before id.EventID
	Limit  int
}

const timelineSQL = `
SELECT e.id, e.type::text, COALESCE(e.payload->>'text', ''), e.occurred_at,
       u.public_code, u.display_name
FROM event e
JOIN service_user u ON u.id = e.user_id
WHERE e.room_id = $1`

// Timeline returns one newest-first page of roomID's timeline — messages and
// membership entries interleaved in id order. Query narrows the page to
// messages whose text contains every whitespace-separated term of the fragment
// (PGroonga full-text over the message-search index; terms match literally —
// see groongaLiteralQuery). Before is the keyset cursor: only events with a
// smaller id are returned. The boolean reports whether at least one more event
// follows the page, determined by fetching Limit+1 rows and trimming the
// look-ahead row.
func (j *Journal) Timeline(ctx context.Context, q postgres.Querier, roomID id.RoomID, params TimelineParams) ([]TimelineEntry, bool, error) {
	query := timelineSQL
	args := []any{roomID.Int64()}
	if params.Before != (id.EventID{}) {
		args = append(args, params.Before.Int64())
		query += fmt.Sprintf(" AND e.id < $%d", len(args))
	}
	if !params.Query.IsZero() {
		args = append(args, groongaLiteralQuery(params.Query))
		query += fmt.Sprintf(" AND e.type = 'message_posted' AND (e.payload->>'text') &@~ $%d", len(args))
	}
	args = append(args, params.Limit+1)
	query += fmt.Sprintf(" ORDER BY e.id DESC LIMIT $%d", len(args))

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("persistence: timeline: %w", err)
	}
	entries, err := collectEntries(rows)
	if err != nil {
		return nil, false, err
	}
	if len(entries) > params.Limit {
		return entries[:params.Limit], true, nil
	}
	return entries, false, nil
}

// groongaLiteralQuery renders the fragment as a Groonga query that matches it
// literally: each whitespace-separated term becomes a quoted phrase (its
// backslashes and double quotes escaped), joined by the implicit AND. Quoting
// keeps operator-looking words (OR, a leading minus) and query syntax in the
// fragment from changing semantics — a fragment can only ever mean "every one
// of these terms appears".
func groongaLiteralQuery(q domain.MessageQuery) string {
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	terms := strings.Fields(q.String())
	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = `"` + escaper.Replace(term) + `"`
	}
	return strings.Join(quoted, " ")
}

// collectEntries scans and parses every row, closing the rows on return.
func collectEntries(rows pgx.Rows) ([]TimelineEntry, error) {
	defer rows.Close()
	var out []TimelineEntry
	for rows.Next() {
		var (
			rawID      int64
			rawKind    string
			text       string
			occurredAt time.Time
			rawCode    string
			name       string
		)
		if err := rows.Scan(&rawID, &rawKind, &text, &occurredAt, &rawCode, &name); err != nil {
			return nil, fmt.Errorf("persistence: scan timeline row: %w", err)
		}
		eventID, err := id.NewEventID(rawID)
		if err != nil {
			return nil, fmt.Errorf("persistence: timeline row %d: %w", rawID, err)
		}
		kind, err := parseEntryKind(rawKind)
		if err != nil {
			return nil, fmt.Errorf("persistence: timeline row %d: %w", rawID, err)
		}
		code, err := id.ParsePublicCode(rawCode)
		if err != nil {
			return nil, fmt.Errorf("persistence: timeline row %d: user public_code: %w", rawID, err)
		}
		out = append(out, TimelineEntry{
			ID:         eventID,
			Kind:       kind,
			User:       TimelineUser{Code: code, Name: name},
			Text:       text,
			OccurredAt: occurredAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("persistence: timeline rows: %w", err)
	}
	return out, nil
}
