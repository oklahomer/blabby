package room

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/journal"
	"github.com/oklahomer/blabby/internal/persistence/membershiprepo"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// MembershipEvent is the identity of the durable membership event appended for a
// join/leave transition: the Snowflake event id and its server-assigned time.
// The Room grain carries both onto the resulting fan-out so a consumer can
// correlate the system line with the room timeline. The zero value means "no
// durable event" — see IsZero.
type MembershipEvent struct {
	ID         id.EventID
	OccurredAt time.Time
}

// IsZero reports whether evt carries no durable event. A real event always has a
// non-zero server-assigned occurred_at, so the timestamp is the discriminator.
func (evt MembershipEvent) IsZero() bool { return evt.OccurredAt.IsZero() }

// MembershipStore is the Room grain's port onto DB-authoritative membership. The
// grain is the sole writer: a join/leave writes the room_membership row and
// appends its derived timeline event in one transaction (fail-closed together),
// and an activation seeds its member cache from LoadMembers.
//
// The implementation owns its own latency budget — callers pass a plain context
// and impose no deadline — mirroring RoomLoader.
type MembershipStore interface {
	// LoadMembers returns the room's current members, for the activation-time
	// member cache. An empty room yields an empty slice, not an error.
	LoadMembers(ctx context.Context, roomID id.RoomID) ([]id.UserRef, error)
	// RecordJoin durably adds actor as a member and appends a member_joined event
	// in one transaction, returning the event identity for fan-out.
	RecordJoin(ctx context.Context, roomID id.RoomID, actor id.UserRef) (MembershipEvent, error)
	// RecordLeave durably removes actor and appends a member_left event in one
	// transaction, returning the event identity for fan-out.
	RecordLeave(ctx context.Context, roomID id.RoomID, actor id.UserRef) (MembershipEvent, error)
}

// membershipOpTimeout bounds a single activation read or transactional write. It
// is owned here (the callee), not by the grain, so a stalled database cannot
// block a grain goroutine indefinitely.
const membershipOpTimeout = 3 * time.Second

// membershipStore is the production MembershipStore: it composes the membership
// repository, the journal, and a transactor over the backend's pool, so a
// room_membership write and its derived event commit (or roll back) together.
type membershipStore struct {
	repo    *membershiprepo.Repo
	journal *journal.Journal
	tx      *postgres.Transactor
	pool    postgres.Querier
}

// NewMembershipStore builds the production MembershipStore over pool, minting
// event ids from ids (the worker-lease manager). Reads run against the pool
// directly; writes run inside a transaction.
func NewMembershipStore(pool *pgxpool.Pool, ids journal.IDSource) MembershipStore {
	return &membershipStore{
		repo:    membershiprepo.New(),
		journal: journal.New(ids),
		tx:      postgres.NewTransactor(pool),
		pool:    pool,
	}
}

func (s *membershipStore) LoadMembers(ctx context.Context, roomID id.RoomID) ([]id.UserRef, error) {
	ctx, cancel := context.WithTimeout(ctx, membershipOpTimeout)
	defer cancel()

	members, err := s.repo.ListByRoom(ctx, s.pool, roomID)
	if err != nil {
		return nil, err
	}
	refs := make([]id.UserRef, len(members))
	for i, m := range members {
		refs[i] = m.User
	}
	return refs, nil
}

func (s *membershipStore) RecordJoin(ctx context.Context, roomID id.RoomID, actor id.UserRef) (MembershipEvent, error) {
	// Grain-initiated joins are ordinary members; owner seeding is the gateway's
	// room-creation path, and role mutation belongs to a later phase.
	return s.record(ctx, roomID, actor, journal.MemberJoined, func(ctx context.Context, q postgres.Querier) error {
		return s.repo.Add(ctx, q, roomID, actor, domain.MembershipRoleMember)
	})
}

func (s *membershipStore) RecordLeave(ctx context.Context, roomID id.RoomID, actor id.UserRef) (MembershipEvent, error) {
	return s.record(ctx, roomID, actor, journal.MemberLeft, func(ctx context.Context, q postgres.Querier) error {
		return s.repo.Remove(ctx, q, roomID, actor.ID())
	})
}

// record runs mutate and the journal append in one transaction, returning the
// appended event's identity. The row write and its derived event commit (or roll
// back) together; on any error nothing is written and a zero event is returned.
// mutate receives the timeout-bounded context so the row write — the lock-taking
// statement most likely to block — is bounded by membershipOpTimeout like the
// append, not left on the caller's (deadline-less) context.
func (s *membershipStore) record(
	ctx context.Context,
	roomID id.RoomID,
	actor id.UserRef,
	kind journal.MemberEventKind,
	mutate func(context.Context, postgres.Querier) error,
) (MembershipEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, membershipOpTimeout)
	defer cancel()

	var evt MembershipEvent
	err := s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		if err := mutate(ctx, q); err != nil {
			return err
		}
		eventID, occurredAt, err := s.journal.AppendMembership(ctx, q, roomID, actor, kind)
		if err != nil {
			return err
		}
		evt = MembershipEvent{ID: eventID, OccurredAt: occurredAt}
		return nil
	})
	if err != nil {
		return MembershipEvent{}, err
	}
	return evt, nil
}
