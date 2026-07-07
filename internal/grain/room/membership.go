package room

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
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

// ErrRolePermissionDenied reports that the acting member's role does not permit
// the requested role mutation: a non-owner/admin changing roles, any attempt to
// move the owner role through a role change, or a non-owner transferring
// ownership. The store decides this against the roles read in the same
// transaction as the write, so the check can never race a concurrent change.
var ErrRolePermissionDenied = errors.New("room: role permission denied")

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
	// transaction, returning the event identity for fan-out. It returns
	// persistence.ErrOwnerCannotLeave (wrapped) when actor owns the room.
	RecordLeave(ctx context.Context, roomID id.RoomID, actor id.UserRef) (MembershipEvent, error)
	// RecordRoleChange durably sets target's role after checking, in the same
	// transaction, that actor's role permits it (domain.CanSetRole). It returns
	// ErrRolePermissionDenied when the policy refuses. Roles are not part of the
	// grain's member cache, so no fan-out or cache update follows.
	RecordRoleChange(ctx context.Context, roomID id.RoomID, actor, target id.UserID, role domain.MembershipRole) error
	// RecordOwnershipTransfer durably hands the room from actor to newOwner
	// (owner -> admin, newOwner -> owner) after checking actor's authority in the
	// same transaction. It returns ErrRolePermissionDenied when actor is not the
	// owner; transferring to the current owner is a successful no-op.
	RecordOwnershipTransfer(ctx context.Context, roomID id.RoomID, actor, newOwner id.UserID) error
}

// membershipOpTimeout bounds a single activation read or transactional write. It
// is owned here (the callee), not by the grain, so a stalled database cannot
// block a grain goroutine indefinitely.
const membershipOpTimeout = 3 * time.Second

// membershipStore is the production MembershipStore: it composes the membership
// repository, the journal, and a transactor over the backend's pool, so a
// room_membership write and its derived event commit (or roll back) together.
type membershipStore struct {
	repo    *persistence.MembershipRepo
	journal *persistence.Journal
	tx      *postgres.Transactor
	pool    postgres.Querier
}

// NewMembershipStore builds the production MembershipStore over pool, minting
// event ids from ids (the worker-lease manager). Reads run against the pool
// directly; writes run inside a transaction.
func NewMembershipStore(pool *pgxpool.Pool, ids persistence.EventIDSource) MembershipStore {
	return &membershipStore{
		repo:    persistence.NewMembershipRepo(),
		journal: persistence.NewJournal(ids),
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
	return s.record(ctx, roomID, actor, persistence.MemberJoined, func(ctx context.Context, q postgres.Querier) error {
		return s.repo.Add(ctx, q, roomID, actor, domain.MembershipRoleMember)
	})
}

func (s *membershipStore) RecordLeave(ctx context.Context, roomID id.RoomID, actor id.UserRef) (MembershipEvent, error) {
	return s.record(ctx, roomID, actor, persistence.MemberLeft, func(ctx context.Context, q postgres.Querier) error {
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
	kind persistence.MemberEventKind,
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

// RecordRoleChange reads both members' roles and applies the role-change policy
// inside the write transaction, so the authorization decision and the update
// commit together. The grain pre-checks membership of both parties against its
// cache, so a missing row here is a cache/DB divergence surfaced as a hard error.
func (s *membershipStore) RecordRoleChange(ctx context.Context, roomID id.RoomID, actor, target id.UserID, role domain.MembershipRole) error {
	ctx, cancel := context.WithTimeout(ctx, membershipOpTimeout)
	defer cancel()

	return s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		actorRole, err := s.repo.GetRole(ctx, q, roomID, actor)
		if err != nil {
			return err
		}
		targetRole, err := s.repo.GetRole(ctx, q, roomID, target)
		if err != nil {
			return err
		}
		if !domain.CanSetRole(actorRole, targetRole, role) {
			return ErrRolePermissionDenied
		}
		return s.repo.UpdateRole(ctx, q, roomID, target, role)
	})
}

// RecordOwnershipTransfer checks the actor's authority and performs the
// demote/promote pair inside one transaction. Handing the room to the current
// owner is a successful no-op, so the operation is idempotent for its caller.
func (s *membershipStore) RecordOwnershipTransfer(ctx context.Context, roomID id.RoomID, actor, newOwner id.UserID) error {
	ctx, cancel := context.WithTimeout(ctx, membershipOpTimeout)
	defer cancel()

	return s.tx.WithinTx(ctx, func(q postgres.Querier) error {
		actorRole, err := s.repo.GetRole(ctx, q, roomID, actor)
		if err != nil {
			return err
		}
		if !domain.CanTransferOwnership(actorRole) {
			return ErrRolePermissionDenied
		}
		if actor == newOwner {
			return nil
		}
		if _, err := s.repo.GetRole(ctx, q, roomID, newOwner); err != nil {
			return err
		}
		return s.repo.TransferOwnership(ctx, q, roomID, actor, newOwner)
	})
}
