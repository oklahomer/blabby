package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/journal"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
)

// RoomCreator creates a room owned by the acting user. The POST /rooms handler
// depends on this interface so it can be unit-tested with a fake;
// RoomCreationService is the production implementation.
type RoomCreator interface {
	CreateRoom(ctx context.Context, actor id.UserID, name domain.RoomName) (RoomInfo, error)
}

// creationRooms, creationUsers, creationMemberships, and creationJournal are the
// narrow repository surfaces room creation composes. Defined here (where they are
// consumed) so the service tests fake repository methods, not SQL.
type creationRooms interface {
	Create(ctx context.Context, q postgres.Querier, params persistence.RoomCreateParams) (persistence.Room, error)
}

type creationUsers interface {
	FindByID(ctx context.Context, q postgres.Querier, userID id.UserID) (persistence.User, error)
}

type creationMemberships interface {
	Add(ctx context.Context, q postgres.Querier, roomID id.RoomID, ref id.UserRef, role domain.MembershipRole) error
}

type creationJournal interface {
	AppendMembership(ctx context.Context, q postgres.Querier, roomID id.RoomID, actor id.UserRef, kind journal.MemberEventKind) (id.EventID, time.Time, error)
}

// roomCreateCollisionRetryLimit bounds re-running the creation transaction when a
// minted public_code collides with an existing room.
const roomCreateCollisionRetryLimit = 3

// RoomCreationService creates rooms: the room row, the creator's owner
// membership, and the founding member_joined timeline event commit in one
// transaction, so a room can never exist ownerless or with an unexplained owner
// in its timeline. (The seeded dev rooms predate their timelines and carry no
// founding event — they are fixtures, not products of this path.)
type RoomCreationService struct {
	rooms       creationRooms
	users       creationUsers
	memberships creationMemberships
	journal     creationJournal
	tx          transactor
}

// NewRoomCreationService builds a RoomCreationService. rooms must mint ids (its
// IDSource is the gateway's worker-lease manager), since creation mints the
// RoomID and public_code; journal mints the founding event's id the same way.
func NewRoomCreationService(rooms creationRooms, users creationUsers, memberships creationMemberships, jrnl creationJournal, tx transactor) *RoomCreationService {
	return &RoomCreationService{
		rooms:       rooms,
		users:       users,
		memberships: memberships,
		journal:     jrnl,
		tx:          tx,
	}
}

// CreateRoom creates an active room owned by actor and returns its descriptor.
// A public_code collision re-runs the whole transaction with a freshly minted
// code, bounded by roomCreateCollisionRetryLimit.
func (s *RoomCreationService) CreateRoom(ctx context.Context, actor id.UserID, name domain.RoomName) (RoomInfo, error) {
	var result RoomInfo

	op := func(q postgres.Querier) error {
		// The creator's display name labels the owner membership's founding
		// member_joined event. The actor is the authenticated caller, so a missing
		// row is a broken contract, not a business outcome.
		user, err := s.users.FindByID(ctx, q, actor)
		if err != nil {
			return fmt.Errorf("room creation: resolve creator: %w", err)
		}
		ownerRef, err := id.NewUserRef(actor, user.PublicCode, user.DisplayName)
		if err != nil {
			return fmt.Errorf("room creation: creator ref: %w", err)
		}

		room, err := s.rooms.Create(ctx, q, persistence.RoomCreateParams{Name: name, CreatedBy: actor})
		if err != nil {
			return err // ErrPublicCodeCollision drives the retry loop; else hard
		}
		if err := s.memberships.Add(ctx, q, room.ID, ownerRef, domain.MembershipRoleOwner); err != nil {
			return fmt.Errorf("room creation: seed owner membership: %w", err)
		}
		if _, _, err := s.journal.AppendMembership(ctx, q, room.ID, ownerRef, journal.MemberJoined); err != nil {
			return fmt.Errorf("room creation: founding event: %w", err)
		}

		result = RoomInfo{ID: room.ID, Code: room.PublicCode, Name: room.DisplayName}
		return nil
	}

	for attempt := 0; ; attempt++ {
		err := s.tx.WithinTx(ctx, op)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, persistence.ErrRoomPublicCodeCollision) {
			return RoomInfo{}, err
		}
		if attempt >= roomCreateCollisionRetryLimit {
			return RoomInfo{}, fmt.Errorf("room creation: public_code collisions exhausted after %d retries: %w", attempt, err)
		}
	}
}
