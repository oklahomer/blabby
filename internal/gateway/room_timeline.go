package gateway

import (
	"context"
	"errors"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/journal"
	"github.com/oklahomer/blabby/internal/persistence/membershiprepo"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// TimelineQuery filters and paginates one room's timeline page. The zero
// values of Query and Before mean "no message-text filter" and "newest page";
// Limit is the page size and must be positive (the handler parses and caps it
// at the boundary).
type TimelineQuery struct {
	RoomID id.RoomID
	Query  domain.MessageQuery
	Before id.EventID
	Limit  int
}

// TimelinePage is one newest-first page of a room's timeline. HasMore reports
// whether at least one older event follows the last entry, so the handler can
// emit a continuation cursor.
type TimelinePage struct {
	Events  []journal.Entry
	HasMore bool
}

// RoomTimeline serves the membership-gated timeline reads behind
// GET /rooms/{id}/events. It is the gateway's seam over the membership table
// and the event journal, so handlers never touch the database. Membership is
// read from the database — the authoritative store the Room grain commits to —
// rather than asking the grain, keeping the read path grain-free.
type RoomTimeline interface {
	// IsMember reports whether userID currently holds any membership in roomID.
	IsMember(ctx context.Context, roomID id.RoomID, userID id.UserID) (bool, error)
	// Events returns one newest-first page of the room's timeline.
	Events(ctx context.Context, query TimelineQuery) (TimelinePage, error)
}

// roomTimelineReader is the production RoomTimeline: membershiprepo and the
// journal over the gateway's read pool. The journal's id source is nil because
// the gateway only reads events, never mints them.
type roomTimelineReader struct {
	members *membershiprepo.Repo
	journal *journal.Journal
	pool    postgres.Querier
}

// NewRoomTimelineReader builds a read-only RoomTimeline over pool.
func NewRoomTimelineReader(pool postgres.Querier) RoomTimeline {
	return roomTimelineReader{
		members: membershiprepo.New(),
		journal: journal.New(nil),
		pool:    pool,
	}
}

func (r roomTimelineReader) IsMember(ctx context.Context, roomID id.RoomID, userID id.UserID) (bool, error) {
	_, err := r.members.GetRole(ctx, r.pool, roomID, userID)
	if errors.Is(err, membershiprepo.ErrMembershipNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r roomTimelineReader) Events(ctx context.Context, query TimelineQuery) (TimelinePage, error) {
	entries, hasMore, err := r.journal.Timeline(ctx, r.pool, query.RoomID, journal.TimelineParams{
		Query:  query.Query,
		Before: query.Before,
		Limit:  query.Limit,
	})
	if err != nil {
		return TimelinePage{}, err
	}
	return TimelinePage{Events: entries, HasMore: hasMore}, nil
}
