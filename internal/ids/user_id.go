package ids

import "fmt"

// UserID identifies an authenticated user across the system. Its value
// is opaque to consumers; only [UserID.String] exposes the underlying
// representation, and only [NewUserID] can construct a non-zero value.
//
// A zero-value UserID (UserID{}) is not a valid identifier. Callers
// reaching one indicate a missing parse step at an earlier boundary —
// the type system cannot prevent the zero value from being declared,
// but the structural rules prevent the constructor from emitting one.
type UserID struct{ value string }

// NewUserID parses raw into a UserID, applying the uniform structural
// rules documented on the package. On failure it returns the zero value
// and an error wrapping one of [ErrEmptyIdentifier],
// [ErrIdentifierTooLong], or [ErrIdentifierInvalidChar].
func NewUserID(raw string) (UserID, error) {
	v, err := parseIdentifier(raw)
	if err != nil {
		return UserID{}, fmt.Errorf("user_id: %w", err)
	}
	return UserID{value: v}, nil
}

// String returns the underlying identifier string. The empty string is
// returned only for the zero value.
func (id UserID) String() string { return id.value }
