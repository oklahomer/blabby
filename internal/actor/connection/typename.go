package connection

import "fmt"

// typeName returns a stable %T-style type name suitable for log fields.
// %T expands to a type, not the value, so it is safe for messages that
// would otherwise carry sensitive content (tokens, message bodies).
func typeName(v any) string {
	if v == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", v)
}
