package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// EventIDSource mints the next Snowflake id for an event. It is the same one-method
// contract the room repo uses, satisfied by the worker-lease Manager, which mints only
// while it holds an unexpired lease (fail-closed).
type EventIDSource interface {
	Next() (int64, error)
}

// Journal appends to and reads the event table. Its methods take a
// postgres.Querier (pool or tx) per call, so the Room grain can append a
// membership event inside the same transaction as the room_membership write.
type Journal struct {
	ids EventIDSource
}

// NewJournal returns a Journal that mints event ids from ids.
func NewJournal(ids EventIDSource) *Journal { return &Journal{ids: ids} }

const appendMessageSQL = `
INSERT INTO event (id, room_id, type, user_id, occurred_at, payload)
VALUES ($1, $2, 'message_posted', $3, now(), $4)
RETURNING occurred_at`

// AppendMessage appends a message_posted event authored by author in roomID and
// returns the new event's id and server-assigned occurred_at. The id is minted
// from the Snowflake source; occurred_at is the DB clock (display-only — the
// timeline orders by id). client_key is left null: send idempotency is a
// separate arc (the schema and unique index are already in place for it).
func (j *Journal) AppendMessage(ctx context.Context, q postgres.Querier, roomID id.RoomID, author id.UserID, text string) (id.EventID, time.Time, error) {
	rawID, err := j.ids.Next()
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: mint event id: %w", err)
	}
	eventID, err := id.NewEventID(rawID)
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: mint event id: %w", err)
	}

	payload, err := json.Marshal(messagePayload{Text: text})
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: marshal payload: %w", err)
	}

	var occurredAt time.Time
	err = q.QueryRow(ctx, appendMessageSQL,
		eventID.Int64(), roomID.Int64(), author.Int64(), payload,
	).Scan(&occurredAt)
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: append message: %w", err)
	}
	return eventID, occurredAt, nil
}

const appendMembershipSQL = `
INSERT INTO event (id, room_id, type, user_id, occurred_at, payload)
VALUES ($1, $2, $3::event_type, $4, now(), $5)
RETURNING occurred_at`

// AppendMembership appends a member_joined / member_left event for actor in
// roomID and returns the new event's id and server-assigned occurred_at. The id
// is minted from the Snowflake source; occurred_at is the DB clock (display-only —
// the timeline orders by id). client_key is left null: system events are not
// deduplicated.
//
// The caller runs this inside the same transaction as the authoritative
// room_membership change, so the row and its derived timeline event commit (or
// roll back) together.
func (j *Journal) AppendMembership(ctx context.Context, q postgres.Querier, roomID id.RoomID, actor domain.UserRef, kind MemberEventKind) (id.EventID, time.Time, error) {
	eventType, err := kind.eventType()
	if err != nil {
		return id.EventID{}, time.Time{}, err
	}

	rawID, err := j.ids.Next()
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: mint event id: %w", err)
	}
	eventID, err := id.NewEventID(rawID)
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: mint event id: %w", err)
	}

	payload, err := json.Marshal(memberEventPayload{DisplayName: actor.Name()})
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: marshal payload: %w", err)
	}

	var occurredAt time.Time
	err = q.QueryRow(ctx, appendMembershipSQL,
		eventID.Int64(), roomID.Int64(), eventType, actor.ID().Int64(), payload,
	).Scan(&occurredAt)
	if err != nil {
		return id.EventID{}, time.Time{}, fmt.Errorf("persistence: append membership: %w", err)
	}
	return eventID, occurredAt, nil
}
