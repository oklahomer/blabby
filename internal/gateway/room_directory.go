package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/roomrepo"
)

// RoomInfo is the gateway's view of a room for client-facing rendering: the
// internal id (to correlate with grain results), the opaque public_code (the R…
// the client sees), and the display name. No internal numeric id is rendered to
// the client — only Code crosses, as R<code>.
type RoomInfo struct {
	ID   id.RoomID
	Code id.PublicCode
	Name string
}

// PublicID renders the room's client-facing R… code.
func (ri RoomInfo) PublicID() string { return ri.Code.FormatRoom() }

// RoomDirectory translates between the opaque, client-facing room codes (R…) and
// internal RoomIDs, and lists rooms for the catalogue and joined-rooms responses.
// It is the gateway's seam over roomrepo, so handlers never touch the database and
// no internal numeric id reaches a client. Resolve reports roomrepo.ErrRoomNotFound
// for an unknown or inactive code.
type RoomDirectory interface {
	Resolve(ctx context.Context, code id.PublicCode) (id.RoomID, error)
	ListActive(ctx context.Context) ([]RoomInfo, error)
	// Describe returns the active rooms for ids in the same order as ids,
	// dropping any unknown or inactive id, so callers render the result directly.
	Describe(ctx context.Context, ids []id.RoomID) ([]RoomInfo, error)
}

// catalogueLimit caps GET /rooms until keyset pagination and a query filter land
// with the discovery work.
const catalogueLimit = 200

// roomRepoDirectory is the production RoomDirectory: a read-only view of the room
// table via roomrepo over the gateway's read pool. The gateway never creates
// rooms, so roomrepo's id source is unused here.
type roomRepoDirectory struct {
	repo *roomrepo.Repo
	pool postgres.Querier
}

// NewRoomRepoDirectory builds a read-only RoomDirectory over pool. It owns the
// roomrepo.Repo internally with a nil id source, because the gateway reads rooms
// but never mints them — so callers never see the unused id source.
func NewRoomRepoDirectory(pool postgres.Querier) RoomDirectory {
	return roomRepoDirectory{repo: roomrepo.New(nil), pool: pool}
}

func (d roomRepoDirectory) Resolve(ctx context.Context, code id.PublicCode) (id.RoomID, error) {
	room, err := d.repo.FindByPublicCode(ctx, d.pool, code)
	if err != nil {
		return id.RoomID{}, err
	}
	return room.ID, nil
}

func (d roomRepoDirectory) ListActive(ctx context.Context) ([]RoomInfo, error) {
	rooms, err := d.repo.ListActive(ctx, d.pool, catalogueLimit)
	if err != nil {
		return nil, err
	}
	return toRoomInfos(rooms), nil
}

func (d roomRepoDirectory) Describe(ctx context.Context, ids []id.RoomID) ([]RoomInfo, error) {
	rooms, err := d.repo.ListByIDs(ctx, d.pool, ids)
	if err != nil {
		return nil, err
	}
	return orderInfosByIDs(ids, toRoomInfos(rooms)), nil
}

func toRoomInfos(rooms []roomrepo.Room) []RoomInfo {
	out := make([]RoomInfo, len(rooms))
	for i, r := range rooms {
		out[i] = RoomInfo{ID: r.ID, Code: r.PublicCode, Name: r.DisplayName}
	}
	return out
}

// orderInfosByIDs returns infos in the order of ids, dropping any id without a
// matching info (an unknown or inactive room). ListByIDs returns rows in an
// arbitrary order, so the directory reimposes the caller's order here.
func orderInfosByIDs(ids []id.RoomID, infos []RoomInfo) []RoomInfo {
	byID := make(map[id.RoomID]RoomInfo, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}
	out := make([]RoomInfo, 0, len(ids))
	for _, roomID := range ids {
		if info, ok := byID[roomID]; ok {
			out = append(out, info)
		}
	}
	return out
}

// roomCodeLookupTimeout bounds a cache-miss DB lookup. It is short and owned here
// (not by the caller) so a stalled database can never block a UserConnection actor
// indefinitely while it resolves a room code for an outbound frame.
const roomCodeLookupTimeout = 3 * time.Second

// roomCodeCache resolves internal room ids to their public R… codes for the
// WebSocket fan-out path, caching the immutable id→code mapping so the
// connection actors do not hit the database on every delivered frame. It
// satisfies connection.RoomCodeResolver and is safe for concurrent use by the
// many connection actors a gateway hosts.
type roomCodeCache struct {
	dir   RoomDirectory
	mu    sync.RWMutex
	codes map[id.RoomID]string
}

// newRoomCodeCache returns a resolver backed by dir.
func newRoomCodeCache(dir RoomDirectory) *roomCodeCache {
	return &roomCodeCache{dir: dir, codes: map[id.RoomID]string{}}
}

// PublicRoomCode maps an internal RoomID to its R… code, consulting the directory
// only on a cache miss. A public_code never changes, so a cached entry is never
// invalidated.
func (c *roomCodeCache) PublicRoomCode(ctx context.Context, roomID id.RoomID) (string, error) {
	c.mu.RLock()
	code, ok := c.codes[roomID]
	c.mu.RUnlock()
	if ok {
		return code, nil
	}

	lookupCtx, cancel := context.WithTimeout(ctx, roomCodeLookupTimeout)
	defer cancel()
	infos, err := c.dir.Describe(lookupCtx, []id.RoomID{roomID})
	if err != nil {
		return "", fmt.Errorf("gateway: resolve room code: %w", err)
	}
	if len(infos) == 0 {
		return "", roomrepo.ErrRoomNotFound
	}
	code = infos[0].PublicID()

	c.mu.Lock()
	c.codes[roomID] = code
	c.mu.Unlock()
	return code, nil
}
