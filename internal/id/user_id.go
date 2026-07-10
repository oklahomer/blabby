package id

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
)

// UserID identifies an authenticated user across the system. It is a Snowflake —
// a positive, time-ordered 63-bit value stored as a BIGINT — wrapped so a
// function expecting a UserID rejects a RoomID, an EventID, or a bare int64 at
// compile time.
//
// A UserID is an internal identifier. Its decimal-string form ([UserID.String])
// is how it serializes into proto fields and JSON and how it binds to a BIGINT
// column — the string form is used wherever it crosses a JSON/JavaScript boundary,
// since JS cannot hold a 63-bit integer exactly. The client-facing user identifier
// is the separate, opaque [PublicCode] (U<code>), resolved to a UserID at the
// gateway; the internal UserID itself is not shown to clients. Parse a
// decimal-string id with [ParseUserID]; wrap a value read from a BIGINT column
// with [NewUserID].
//
// A zero-value UserID (UserID{}) is not a valid identifier. Callers reaching one
// indicate a missing parse step at an earlier boundary — the type system cannot
// prevent the zero value from being declared, but the constructors never emit one.
type UserID struct{ value int64 }

// ErrInvalidUserID reports a non-positive UserID. A valid Snowflake is always
// positive, so zero or negative means a missing or corrupted parse upstream.
var ErrInvalidUserID = errors.New("user_id: must be a positive snowflake")

// NewUserID wraps a raw Snowflake value, rejecting non-positive inputs. Use it
// for a value read from a BIGINT column or minted by the snowflake generator.
func NewUserID(v int64) (UserID, error) {
	if v <= 0 {
		return UserID{}, ErrInvalidUserID
	}
	return UserID{value: v}, nil
}

// ParseUserID decodes the decimal-string form carried in a proto field or other
// internal serialization into a UserID.
func ParseUserID(s string) (UserID, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return UserID{}, fmt.Errorf("user_id: %w", err)
	}
	return NewUserID(v)
}

// Int64 returns the underlying Snowflake, e.g. for binding to a BIGINT column.
func (u UserID) Int64() int64 { return u.value }

// IsZero reports whether u is the zero value — the invalid placeholder no
// constructor emits. Following the [time.Time.IsZero] precedent, it lets a
// caller ask "has an id been assigned yet?" without comparing against a
// struct literal.
func (u UserID) IsZero() bool { return u.value == 0 }

// String renders the id as a decimal string — its on-the-wire form. The zero
// value renders as "0", which no valid id equals.
func (u UserID) String() string { return strconv.FormatInt(u.value, 10) }

// LogValue implements slog.LogValuer so handlers render the identifier as its
// decimal string without call sites having to add .String(). encoding/json does
// not honor fmt.Stringer; LogValuer is the slog-specific bridge that does.
func (u UserID) LogValue() slog.Value { return slog.StringValue(u.String()) }

// MarshalJSON encodes the id as a decimal string so 63-bit values survive JSON
// and JavaScript number handling intact.
func (u UserID) MarshalJSON() ([]byte, error) {
	return json.Marshal(u.String())
}

// UnmarshalJSON parses a decimal-string id. A bare JSON number is rejected so the
// wire form stays unambiguous and lossless on the client side.
func (u *UserID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("user_id: expected a decimal string: %w", err)
	}
	parsed, err := ParseUserID(s)
	if err != nil {
		return err
	}
	*u = parsed
	return nil
}
