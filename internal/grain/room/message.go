package room

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/journal"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// MessageEvent is the identity of the durable message_posted event appended for
// a chat message: the Snowflake event id and its server-assigned time. The Room
// grain uses the timestamp for the response and fan-out so live frames agree
// with the room timeline. The zero value means "no durable event" — see IsZero.
type MessageEvent struct {
	ID         id.EventID
	OccurredAt time.Time
}

// IsZero reports whether evt carries no durable event. A real event always has a
// non-zero server-assigned occurred_at, so the timestamp is the discriminator.
func (evt MessageEvent) IsZero() bool { return evt.OccurredAt.IsZero() }

// MessageStore is the Room grain's port onto the durable room timeline for chat
// messages. The grain is the sole appender per room, and the append is
// fail-closed: a message that is not durable is never cached or fanned out.
//
// The implementation owns its own latency budget — callers pass a plain context
// and impose no deadline — mirroring MembershipStore and RoomLoader.
type MessageStore interface {
	// RecordMessage durably appends a message_posted event authored by author
	// and returns the event identity for the response and fan-out timestamps.
	RecordMessage(ctx context.Context, roomID id.RoomID, author id.UserID, text string) (MessageEvent, error)
}

// messageOpTimeout bounds a single message append. It is owned here (the
// callee), not by the grain, so a stalled database cannot block a grain
// goroutine indefinitely.
const messageOpTimeout = 3 * time.Second

// messageStore is the production MessageStore: the journal over the backend's
// pool. A message append is a single INSERT, so no transaction is involved.
type messageStore struct {
	journal *journal.Journal
	pool    postgres.Querier
}

// NewMessageStore builds the production MessageStore over pool, minting event
// ids from ids (the worker-lease manager).
func NewMessageStore(pool *pgxpool.Pool, ids journal.IDSource) MessageStore {
	return &messageStore{journal: journal.New(ids), pool: pool}
}

func (s *messageStore) RecordMessage(ctx context.Context, roomID id.RoomID, author id.UserID, text string) (MessageEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, messageOpTimeout)
	defer cancel()

	eventID, occurredAt, err := s.journal.AppendMessage(ctx, s.pool, roomID, author, text)
	if err != nil {
		return MessageEvent{}, err
	}
	return MessageEvent{ID: eventID, OccurredAt: occurredAt}, nil
}
