package graintest

import (
	"fmt"
	"strconv"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
)

// This file holds the Room request factories used across grain unit and
// integration tests. Add fixtures only when multiple callers need them.

const maxFixturePublicCodeNumber int64 = 10_000_000_000

// BarePublicCodeFor returns a deterministic valid bare public_code for a
// decimal user id, so a fixture UserRef satisfies the Room grain's
// public-code requirement and its rendered U… code is predictable in frame
// assertions. A non-numeric id (used by the reject-path tests, where the id
// itself fails to parse before the code is read) gets a fixed valid stand-in.
func BarePublicCodeFor(userID string) string {
	if n, err := strconv.ParseInt(userID, 10, 64); err == nil && n >= 0 && n < maxFixturePublicCodeNumber {
		return fmt.Sprintf("%010d", n)
	}
	return "0000000000"
}

// NewJoinRequest returns a roompb.JoinRequest for the given user, defaulting
// the display name to the id. Use NewJoinRequestNamed to set a distinct name.
func NewJoinRequest(userID string) *roompb.JoinRequest {
	return NewJoinRequestNamed(userID, userID)
}

// NewJoinRequestNamed returns a roompb.JoinRequest carrying a distinct id and
// display name.
func NewJoinRequestNamed(userID, name string) *roompb.JoinRequest {
	return &roompb.JoinRequest{User: userRefProto(userID, name)}
}

// NewPostMessageRequest returns a roompb.PostMessageRequest for the given
// sender (name defaults to the id) and message text.
func NewPostMessageRequest(userID, text string) *roompb.PostMessageRequest {
	return NewPostMessageRequestNamed(userID, userID, text)
}

// NewPostMessageRequestNamed returns a roompb.PostMessageRequest carrying a
// distinct sender id and display name.
func NewPostMessageRequestNamed(userID, name, text string) *roompb.PostMessageRequest {
	return &roompb.PostMessageRequest{User: userRefProto(userID, name), Text: text}
}

// userRefProto builds the fixture proto UserRef with a deterministic public
// code so grain command paths (which now require one) accept it.
func userRefProto(userID, name string) *commonpb.UserRef {
	return &commonpb.UserRef{Id: userID, Name: name, PublicCode: BarePublicCodeFor(userID)}
}
