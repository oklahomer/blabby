package id

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
)

// EventID identifies a single timeline event (a posted message or a membership
// system entry). It is a Snowflake — a positive, time-ordered 63-bit value — and,
// unlike [UserID] and [RoomID], has no public copy-paste form: it crosses JSON and
// WebSocket as a decimal string, because JavaScript cannot hold a 63-bit integer
// exactly. The decimal string is a programmatic cursor/id, never human-shared.
//
// A zero-value EventID is not valid; only [NewEventID] constructs one.
type EventID struct{ value int64 }

// ErrInvalidEventID reports a non-positive EventID. A valid Snowflake is always
// positive, so zero or negative means a missing or corrupted parse upstream.
var ErrInvalidEventID = errors.New("event_id: must be a positive snowflake")

// NewEventID wraps a raw Snowflake value, rejecting non-positive inputs.
func NewEventID(v int64) (EventID, error) {
	if v <= 0 {
		return EventID{}, ErrInvalidEventID
	}
	return EventID{value: v}, nil
}

// ParseEventID decodes the decimal-string form — the on-the-wire event id and
// timeline cursor — into an EventID.
func ParseEventID(s string) (EventID, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return EventID{}, fmt.Errorf("event_id: %w", err)
	}
	return NewEventID(v)
}

// Int64 returns the underlying Snowflake, e.g. for binding to a BIGINT column.
func (e EventID) Int64() int64 { return e.value }

// IsZero reports whether e is the zero value — the invalid placeholder no
// constructor emits. Callers use it to detect an unset optional id, e.g. a
// timeline query with no Before cursor.
func (e EventID) IsZero() bool { return e.value == 0 }

// String renders the id as a decimal string — its on-the-wire form.
func (e EventID) String() string { return strconv.FormatInt(e.value, 10) }

// LogValue renders the id as its decimal string in structured logs.
func (e EventID) LogValue() slog.Value { return slog.StringValue(e.String()) }

// MarshalJSON encodes the id as a decimal string so 63-bit values survive JSON
// and JavaScript number handling intact.
func (e EventID) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.String())
}

// UnmarshalJSON parses a decimal string id. A bare JSON number is rejected so the
// wire form stays unambiguous (and lossless on the client side).
func (e *EventID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("event_id: expected a decimal string: %w", err)
	}
	parsed, err := ParseEventID(s)
	if err != nil {
		return err
	}
	*e = parsed
	return nil
}
