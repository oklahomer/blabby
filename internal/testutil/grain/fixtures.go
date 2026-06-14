package graintest

import (
	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
)

// This file holds the Room request factories used across grain unit and
// integration tests. Add fixtures only when multiple callers need them.

// NewJoinRequest returns a roompb.JoinRequest for the given user, defaulting
// the display name to the id. Use NewJoinRequestNamed to set a distinct name.
func NewJoinRequest(userID string) *roompb.JoinRequest {
	return NewJoinRequestNamed(userID, userID)
}

// NewJoinRequestNamed returns a roompb.JoinRequest carrying a distinct id and
// display name.
func NewJoinRequestNamed(userID, name string) *roompb.JoinRequest {
	return &roompb.JoinRequest{User: &commonpb.UserRef{Id: userID, Name: name}}
}

// NewPostMessageRequest returns a roompb.PostMessageRequest for the given
// sender (name defaults to the id) and message text.
func NewPostMessageRequest(userID, text string) *roompb.PostMessageRequest {
	return NewPostMessageRequestNamed(userID, userID, text)
}

// NewPostMessageRequestNamed returns a roompb.PostMessageRequest carrying a
// distinct sender id and display name.
func NewPostMessageRequestNamed(userID, name, text string) *roompb.PostMessageRequest {
	return &roompb.PostMessageRequest{User: &commonpb.UserRef{Id: userID, Name: name}, Text: text}
}
