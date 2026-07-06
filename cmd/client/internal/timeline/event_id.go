// Package timeline holds small client-side timeline value types shared by the
// API parser, app state, and render panes.
package timeline

import (
	"errors"
	"fmt"
	"strconv"
)

// EventID is the parsed decimal Snowflake id carried by timeline HTTP entries
// and live WebSocket frames. It orders and dedups scrollback entries numerically;
// the wire form stays a string because JavaScript cannot safely carry 63-bit
// integers as numbers.
type EventID int64

// ErrInvalidEventID reports a non-positive event id. A valid Snowflake is
// always positive, so zero or negative means the wire value was missing or
// corrupt.
var ErrInvalidEventID = errors.New("event_id: must be a positive snowflake")

// NewEventID wraps a raw Snowflake value, rejecting non-positive inputs.
func NewEventID(v int64) (EventID, error) {
	if v <= 0 {
		return 0, ErrInvalidEventID
	}
	return EventID(v), nil
}

// ParseEventID decodes the decimal-string wire form into an EventID.
func ParseEventID(s string) (EventID, error) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("event_id: %w", err)
	}
	return NewEventID(v)
}

// Int64 returns the underlying Snowflake value.
func (e EventID) Int64() int64 { return int64(e) }

// String renders the event id as its decimal wire/cursor form.
func (e EventID) String() string { return strconv.FormatInt(int64(e), 10) }
