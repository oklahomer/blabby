package id

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
)

// RoomID identifies a chat room across the system. It is a Snowflake — a
// positive, time-ordered 63-bit value stored as a BIGINT — wrapped so a function
// expecting a RoomID rejects a UserID, an EventID, or a bare int64 at compile
// time.
//
// A RoomID is an internal identifier. Its decimal-string form ([RoomID.String])
// is how it serializes into proto fields and JSON and how it binds to a BIGINT
// column — the string form is used wherever it crosses a JSON/JavaScript boundary,
// since JS cannot hold a 63-bit integer exactly. The client-facing room identifier
// is the separate, opaque [PublicCode] (R<code>), resolved to a RoomID at the
// gateway; the internal RoomID itself is not shown to clients. Parse a
// decimal-string id with [ParseRoomID]; wrap a value read from a BIGINT column
// with [NewRoomID].
//
// A zero-value RoomID (RoomID{}) is not a valid identifier. Callers reaching one
// indicate a missing parse step at an earlier boundary — the type system cannot
// prevent the zero value from being declared, but the constructors never emit one.
type RoomID struct{ value int64 }

// ErrInvalidRoomID reports a non-positive RoomID. A valid Snowflake is always
// positive, so zero or negative means a missing or corrupted parse upstream.
var ErrInvalidRoomID = errors.New("room_id: must be a positive snowflake")

// NewRoomID wraps a raw Snowflake value, rejecting non-positive inputs. Use it
// for a value read from a BIGINT column or minted by the snowflake generator.
func NewRoomID(v int64) (RoomID, error) {
	if v <= 0 {
		return RoomID{}, ErrInvalidRoomID
	}
	return RoomID{value: v}, nil
}

// ParseRoomID decodes the decimal-string form carried in a proto field or other
// internal serialization into a RoomID.
func ParseRoomID(s string) (RoomID, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return RoomID{}, fmt.Errorf("room_id: %w", err)
	}
	return NewRoomID(v)
}

// Int64 returns the underlying Snowflake, e.g. for binding to a BIGINT column.
func (r RoomID) Int64() int64 { return r.value }

// String renders the id as a decimal string — its on-the-wire form. The zero
// value renders as "0", which no valid id equals.
func (r RoomID) String() string { return strconv.FormatInt(r.value, 10) }

// LogValue implements slog.LogValuer so handlers render the identifier as its
// decimal string without call sites having to add .String(). encoding/json does
// not honor fmt.Stringer; LogValuer is the slog-specific bridge that does.
func (r RoomID) LogValue() slog.Value { return slog.StringValue(r.String()) }

// MarshalJSON encodes the id as a decimal string so 63-bit values survive JSON
// and JavaScript number handling intact.
func (r RoomID) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

// UnmarshalJSON parses a decimal-string id. A bare JSON number is rejected so the
// wire form stays unambiguous and lossless on the client side.
func (r *RoomID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("room_id: expected a decimal string: %w", err)
	}
	parsed, err := ParseRoomID(s)
	if err != nil {
		return err
	}
	*r = parsed
	return nil
}
