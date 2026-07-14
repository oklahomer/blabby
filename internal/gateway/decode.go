package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

// requestError carries both the user-facing gateway ErrorDetail and a
// coarse classifier for the structured-log "reason" field. It is produced
// by the request parsers (decodeStrictJSONBody, decodeSendMessageRequest,
// parseRoomListQuery).
//
// The classifier is distinct from the canonical status string so
// operators can grep logs by cause ("malformed_body" vs "trailing_garbage"
// vs "empty_text" etc.) — the status string alone collapses every 400
// to "INVALID_REQUEST".
type requestError struct {
	reason string
	detail ErrorDetail
}

func (e *requestError) Error() string { return e.detail.Message }

// decodeJSONBody decodes a JSON request body with the gateway's strict rules
// (see decodeStrictJSONBody), writing the rejection itself and returning
// false on failure. It is the one-call entry point for handlers with no
// endpoint-specific rejection logging.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) bool {
	if err := decodeStrictJSONBody(w, r, maxBytes, dst); err != nil {
		WriteErrorResponse(w, httpStatus(err.detail.Code), err.detail)
		return false
	}
	return true
}

// decodeStrictJSONBody applies the gateway's common JSON-body rules: JSON
// content type, a per-endpoint byte cap, exactly one JSON value, and no
// trailing data. Endpoint-specific semantic validation happens in the caller
// after a typed request value exists.
//
// The byte cap holds regardless of which read crosses it: a MaxBytesError
// surfacing through either decode — the value itself or the trailing-data
// check — is an oversize rejection (413), never a generic 400.
func decodeStrictJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) *requestError {
	if !contentTypeIsJSON(r.Header.Get("Content-Type")) {
		return &requestError{
			reason: "content_type",
			detail: ErrInvalidRequest("content-type must be application/json"),
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &requestError{
				reason: "payload_too_large",
				detail: ErrPayloadTooLarge("request body exceeds maximum size"),
			}
		}
		return &requestError{
			reason: "malformed_body",
			detail: ErrInvalidRequest("malformed request body"),
		}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &requestError{
				reason: "payload_too_large",
				detail: ErrPayloadTooLarge("request body exceeds maximum size"),
			}
		}
		return &requestError{
			reason: "trailing_garbage",
			detail: ErrInvalidRequest("malformed request body"),
		}
	}
	return nil
}

// contentTypeIsJSON parses the Content-Type header and returns true if
// the media type is application/json. Charset variants are accepted.
func contentTypeIsJSON(header string) bool {
	if header == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}
