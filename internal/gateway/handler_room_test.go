package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	userpb "github.com/oklahomer/blabby/gen/user"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/errcode"
	"github.com/oklahomer/blabby/internal/id"
)

// sentinelCluster / sentinelActorRoot return non-nil zero-value
// instances purely to satisfy the triple-nil dependency guard. Tests
// that exercise handlers behind the userGrainCaller seam never reach
// any cluster method, so the empty values are safe.
func sentinelCluster() *cluster.Cluster     { return new(cluster.Cluster) }
func sentinelActorRoot() *actor.RootContext { return new(actor.RootContext) }

// withUserContext returns a request with the userID injected via the
// auth context — mimicking what authMiddleware does in production.
func withUserContext(t *testing.T, req *http.Request, userID string) *http.Request {
	t.Helper()
	if userID == "" {
		return req
	}
	return req.WithContext(auth.ContextWithUserID(req.Context(), mustUserID(t, userID)))
}

// gatewayWithFake builds a Gateway whose userGrainFor returns the given
// fake. cluster and actorRoot are non-nil sentinels so the triple-nil
// guard does not fire; the fields are typed pointers and remain unused
// because the seam intercepts the call before any cluster method runs.
func gatewayWithFake(fake userGrainCaller) *Gateway {
	return &Gateway{
		auth:      &stubAuthenticator{},
		rooms:     newStubRoomDirectory(),
		users:     newStubUserResolver(),
		cluster:   sentinelCluster(),
		actorRoot: sentinelActorRoot(),
		userGrain: func(id.UserID) userGrainCaller { return fake },
	}
}

// servePath dispatches the request through a one-route mux so PathValue
// captures {id} the same way the production mux does.
func servePath(t *testing.T, g *Gateway, method, pattern, path, body, contentType, userID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	switch pattern {
	case "PUT /rooms/{id}/membership":
		mux.HandleFunc(pattern, g.handleRoomMembershipPut)
	case "DELETE /rooms/{id}/membership":
		mux.HandleFunc(pattern, g.handleRoomMembershipDelete)
	case "POST /rooms/{id}/messages":
		mux.HandleFunc(pattern, g.handleRoomSendMessage)
	case "PUT /rooms/{id}/members/{user}/role":
		mux.HandleFunc(pattern, g.handleRoomMemberRolePut)
	case "PUT /rooms/{id}/owner":
		mux.HandleFunc(pattern, g.handleRoomOwnerPut)
	case "POST /rooms":
		mux.HandleFunc(pattern, g.handleRoomCreate)
	default:
		t.Fatalf("unsupported pattern: %q", pattern)
	}

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req = withUserContext(t, req, userID)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func decodeSuccess(t *testing.T, body io.Reader, into any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		t.Fatalf("decode success body: %v", err)
	}
}

// captureSlog redirects slog output to a buffer for the duration of fn,
// then returns the captured bytes. Used to assert the NFR1 logging
// invariants (text never logged, text_len always logged).
func captureSlog(t *testing.T, fn func()) []byte {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.Bytes()
}

// ---- handleRoomMembershipPut ------------------------------------------

func TestHandleRoomMembershipPut(t *testing.T) {
	const okUser = "1"
	const okRoomCode = "RG000000004"

	tests := []struct {
		name        string
		path        string
		userID      string
		stubResp    *userpb.JoinRoomResponse
		stubErr     error
		wantStatus  int
		wantCode    errcode.Code
		assertCalls func(t *testing.T, fake *fakeUserGrainCaller)
	}{
		{
			name:       "happy path returns 200 success",
			path:       "/rooms/" + okRoomCode + "/membership",
			userID:     okUser,
			stubResp:   &userpb.JoinRoomResponse{},
			wantStatus: http.StatusOK,
			assertCalls: func(t *testing.T, f *fakeUserGrainCaller) {
				if f.joinReq == nil || f.joinReq.GetRoomId() != "4" {
					t.Fatalf("expected JoinRoom called with room_id=%q, got %+v", "4", f.joinReq)
				}
			},
		},
		{
			name:   "business error 2001 → 403",
			path:   "/rooms/" + okRoomCode + "/membership",
			userID: okUser,
			stubResp: &userpb.JoinRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member",
			}},
			wantStatus: http.StatusForbidden,
			wantCode:   2001,
		},
		{
			name:   "business error 2002 → 409",
			path:   "/rooms/" + okRoomCode + "/membership",
			userID: okUser,
			stubResp: &userpb.JoinRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 2002, Status: "ROOM_ALREADY_MEMBER", Message: "already member",
			}},
			wantStatus: http.StatusConflict,
			wantCode:   2002,
		},
		{
			name:   "business error 2003 → 404",
			path:   "/rooms/" + okRoomCode + "/membership",
			userID: okUser,
			stubResp: &userpb.JoinRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 2003, Status: "ROOM_NOT_FOUND", Message: "no such room",
			}},
			wantStatus: http.StatusNotFound,
			wantCode:   2003,
		},
		{
			name:   "business error 4001 → 400",
			path:   "/rooms/" + okRoomCode + "/membership",
			userID: okUser,
			stubResp: &userpb.JoinRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 4001, Status: "INVALID_REQUEST", Message: "bad",
			}},
			wantStatus: http.StatusBadRequest,
			wantCode:   4001,
		},
		{
			name:   "mismatched business error fails closed",
			path:   "/rooms/" + okRoomCode + "/membership",
			userID: okUser,
			stubResp: &userpb.JoinRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 2001, Status: "ROOM_NOT_FOUND", Message: "bad pair",
			}},
			wantStatus: http.StatusInternalServerError,
			wantCode:   5001,
		},
		{
			name:       "transport error → 503 + 5002",
			path:       "/rooms/" + okRoomCode + "/membership",
			userID:     okUser,
			stubErr:    errors.New("cluster down"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   5002,
		},
		{
			name:       "missing user_id in context → 500 + 5001",
			path:       "/rooms/" + okRoomCode + "/membership",
			userID:     "",
			stubResp:   &userpb.JoinRoomResponse{},
			wantStatus: http.StatusInternalServerError,
			wantCode:   5001,
			assertCalls: func(t *testing.T, f *fakeUserGrainCaller) {
				if f.calls != 0 {
					t.Fatalf("expected no grain calls when auth context missing, got %d", f.calls)
				}
			},
		},
		{
			name:       "URL-encoded space room_id → 400 + 4001",
			path:       "/rooms/%20/membership",
			userID:     okUser,
			stubResp:   &userpb.JoinRoomResponse{},
			wantStatus: http.StatusBadRequest,
			wantCode:   4001,
			assertCalls: func(t *testing.T, f *fakeUserGrainCaller) {
				if f.calls != 0 {
					t.Fatalf("expected no grain call for whitespace id, got %d", f.calls)
				}
			},
		},
		{
			name:       "URL-encoded ideographic space room_id → 400 + 4001",
			path:       "/rooms/%E3%80%80/membership",
			userID:     okUser,
			stubResp:   &userpb.JoinRoomResponse{},
			wantStatus: http.StatusBadRequest,
			wantCode:   4001,
		},
		{
			name:       "malformed room code → 400 + 4001",
			path:       "/rooms/not-a-number/membership",
			userID:     okUser,
			stubResp:   &userpb.JoinRoomResponse{},
			wantStatus: http.StatusBadRequest,
			wantCode:   4001,
		},
		{
			name:       "valid but unknown room code → 404 + 2003",
			path:       "/rooms/RZ000000099/membership",
			userID:     okUser,
			stubResp:   &userpb.JoinRoomResponse{},
			wantStatus: http.StatusNotFound,
			wantCode:   2003,
			assertCalls: func(t *testing.T, f *fakeUserGrainCaller) {
				if f.calls != 0 {
					t.Fatalf("expected no grain call for an unresolved room, got %d", f.calls)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeUserGrainCaller{joinResp: tt.stubResp, joinErr: tt.stubErr}
			g := gatewayWithFake(fake)

			rec := servePath(t, g, http.MethodPut, "PUT /rooms/{id}/membership", tt.path, "", "", tt.userID)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				var resp successResponse
				decodeSuccess(t, rec.Body, &resp)
				if !resp.Success {
					t.Fatalf("expected success=true, got %+v", resp)
				}
			} else {
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tt.wantCode {
					t.Errorf("error.code = %d, want %d", resp.Error.Code, tt.wantCode)
				}
			}
			if tt.assertCalls != nil {
				tt.assertCalls(t, fake)
			}
		})
	}
}

// ---- handleRoomMembershipDelete ---------------------------------------

func TestHandleRoomMembershipDelete(t *testing.T) {
	const okUser = "1"
	const okRoomCode = "RG000000004"

	tests := []struct {
		name       string
		stubResp   *userpb.LeaveRoomResponse
		stubErr    error
		wantStatus int
		wantCode   errcode.Code
	}{
		{
			name:       "happy path",
			stubResp:   &userpb.LeaveRoomResponse{},
			wantStatus: http.StatusOK,
		},
		{
			name: "business error 2001 → 403",
			stubResp: &userpb.LeaveRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member",
			}},
			wantStatus: http.StatusForbidden,
			wantCode:   2001,
		},
		{
			name: "unknown business error fails closed",
			stubResp: &userpb.LeaveRoomResponse{Error: &commonpb.ErrorDetail{
				Code: 9999, Status: "UNKNOWN_ERROR", Message: "bad code",
			}},
			wantStatus: http.StatusInternalServerError,
			wantCode:   5001,
		},
		{
			name:       "transport error → 503",
			stubErr:    errors.New("cluster down"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   5002,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeUserGrainCaller{leaveResp: tt.stubResp, leaveErr: tt.stubErr}
			g := gatewayWithFake(fake)
			rec := servePath(t, g, http.MethodDelete, "DELETE /rooms/{id}/membership",
				"/rooms/"+okRoomCode+"/membership", "", "", okUser)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK {
				var resp successResponse
				decodeSuccess(t, rec.Body, &resp)
				if !resp.Success {
					t.Fatalf("expected success=true, got %+v", resp)
				}
				if fake.leaveReq == nil || fake.leaveReq.GetRoomId() != "4" {
					t.Fatalf("expected LeaveRoom called with room_id=%q, got %+v", "4", fake.leaveReq)
				}
			} else if tt.wantCode != 0 {
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tt.wantCode {
					t.Errorf("error.code = %d, want %d", resp.Error.Code, tt.wantCode)
				}
			}
		})
	}
}

// ---- handleRoomSendMessage --------------------------------------------

func TestHandleRoomSendMessage_HappyPath(t *testing.T) {
	const okUser = "1"
	const okRoomCode = "RG000000004"
	const okText = "hi"
	wantTSMillis := int64(1234567890)

	fake := &fakeUserGrainCaller{sendResp: &userpb.SendMessageResponse{
		Timestamp: timestamppb.New(time.UnixMilli(wantTSMillis)),
	}}
	g := gatewayWithFake(fake)
	rec := servePath(t, g, http.MethodPost, "POST /rooms/{id}/messages",
		"/rooms/"+okRoomCode+"/messages", `{"text":"`+okText+`"}`, "application/json", okUser)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp sendMessageSuccessResponse
	decodeSuccess(t, rec.Body, &resp)
	if !resp.Success {
		t.Errorf("success = false, want true")
	}
	if resp.Timestamp != wantTSMillis {
		t.Errorf("timestamp = %d, want %d", resp.Timestamp, wantTSMillis)
	}
	if fake.sendReq == nil || fake.sendReq.GetText() != okText || fake.sendReq.GetRoomId() != "4" {
		t.Fatalf("SendMessage called with %+v, want room=%q text=%q", fake.sendReq, "4", okText)
	}
}

func TestHandleRoomSendMessage_ForwardsCanonicalText(t *testing.T) {
	// The grain receives the canonical form: CRLF mapped to LF and NFD
	// composed to NFC — not the raw client bytes.
	fake := &fakeUserGrainCaller{sendResp: &userpb.SendMessageResponse{
		Timestamp: timestamppb.New(time.UnixMilli(1)),
	}}
	g := gatewayWithFake(fake)
	rec := servePath(t, g, http.MethodPost, "POST /rooms/{id}/messages",
		"/rooms/RG000000004/messages", `{"text":"line one\r\ncafé"}`, "application/json", "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	const want = "line one\ncafé"
	if fake.sendReq == nil || fake.sendReq.GetText() != want {
		t.Fatalf("SendMessage called with %+v, want text %q", fake.sendReq, want)
	}
}

func TestHandleRoomSendMessage_NilTimestampWritesZero(t *testing.T) {
	fake := &fakeUserGrainCaller{sendResp: &userpb.SendMessageResponse{}}
	g := gatewayWithFake(fake)
	rec := servePath(t, g, http.MethodPost, "POST /rooms/{id}/messages",
		"/rooms/RG000000004/messages", `{"text":"hi"}`, "application/json", "1")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp sendMessageSuccessResponse
	decodeSuccess(t, rec.Body, &resp)
	if resp.Timestamp != 0 {
		t.Errorf("timestamp = %d, want 0 for nil proto timestamp", resp.Timestamp)
	}
}

func TestHandleRoomSendMessage_BusinessAndTransportErrors(t *testing.T) {
	tests := []struct {
		name       string
		stubResp   *userpb.SendMessageResponse
		stubErr    error
		wantStatus int
		wantCode   errcode.Code
	}{
		{
			name: "business 2001 → 403",
			stubResp: &userpb.SendMessageResponse{Error: &commonpb.ErrorDetail{
				Code: 2001, Status: "ROOM_NOT_MEMBER", Message: "not a member",
			}},
			wantStatus: http.StatusForbidden,
			wantCode:   2001,
		},
		{
			name: "business 4001 → 400",
			stubResp: &userpb.SendMessageResponse{Error: &commonpb.ErrorDetail{
				Code: 4001, Status: "INVALID_REQUEST", Message: "bad text",
			}},
			wantStatus: http.StatusBadRequest,
			wantCode:   4001,
		},
		{
			name: "mismatched business error fails closed",
			stubResp: &userpb.SendMessageResponse{Error: &commonpb.ErrorDetail{
				Code: 4001, Status: "MISSING_FIELD", Message: "bad pair",
			}},
			wantStatus: http.StatusInternalServerError,
			wantCode:   5001,
		},
		{
			name:       "transport → 503 + 5002",
			stubErr:    errors.New("cluster down"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   5002,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeUserGrainCaller{sendResp: tt.stubResp, sendErr: tt.stubErr}
			g := gatewayWithFake(fake)
			rec := servePath(t, g, http.MethodPost, "POST /rooms/{id}/messages",
				"/rooms/RG000000004/messages", `{"text":"hi"}`, "application/json", "1")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != tt.wantCode {
				t.Errorf("error.code = %d, want %d", resp.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestHandleRoomSendMessage_BodyValidation(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
		wantCode    errcode.Code
		wantMessage string // substring match
	}{
		{
			name:        "missing content-type → 400",
			body:        `{"text":"hi"}`,
			contentType: "",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "content-type",
		},
		{
			name:        "wrong content-type → 400",
			body:        `{"text":"hi"}`,
			contentType: "text/plain",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "content-type",
		},
		{
			name:        "json with charset accepted",
			body:        `{"text":"hi"}`,
			contentType: "application/json; charset=utf-8",
			wantStatus:  http.StatusOK,
		},
		{
			name:        "malformed JSON → 400",
			body:        `{`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "malformed",
		},
		{
			name:        "trailing JSON → 400",
			body:        `{"text":"hi"}{"x":1}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "malformed",
		},
		{
			name:        "empty text → 400",
			body:        `{"text":"   "}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "text is required",
		},
		{
			name:        "text over 4 KiB → 400",
			body:        `{"text":"` + strings.Repeat("a", domain.MaxMessageTextBytes+1) + `"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "text must be at most 4096 bytes",
		},
		{
			name:        "text with control characters → 400",
			body:        `{"text":"a\u0000b"}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
			wantCode:    4001,
			wantMessage: "text must be at most 4096 bytes",
		},
		{
			name:        "body over MaxBytesReader cap → 413",
			body:        `{"text":"` + strings.Repeat("a", maxRoomMessageBodyBytes+10) + `"}`,
			contentType: "application/json",
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    4003,
			wantMessage: "request body exceeds maximum size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeUserGrainCaller{sendResp: &userpb.SendMessageResponse{
				Timestamp: timestamppb.New(time.UnixMilli(1)),
			}}
			g := gatewayWithFake(fake)
			rec := servePath(t, g, http.MethodPost, "POST /rooms/{id}/messages",
				"/rooms/RG000000004/messages", tt.body, tt.contentType, "1")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				return
			}
			resp := decodeErrorResponse(t, rec.Body)
			if resp.Error.Code != tt.wantCode {
				t.Errorf("error.code = %d, want %d", resp.Error.Code, tt.wantCode)
			}
			if tt.wantMessage != "" && !strings.Contains(resp.Error.Message, tt.wantMessage) {
				t.Errorf("error.message = %q, want substring %q", resp.Error.Message, tt.wantMessage)
			}
		})
	}
}

func TestHandleRoomSendMessage_LoggingNFR1(t *testing.T) {
	const secretText = "do-not-log-this-secret-text-payload"
	const bearerToken = "tok-must-not-leak"

	fake := &fakeUserGrainCaller{sendResp: &userpb.SendMessageResponse{
		Timestamp: timestamppb.New(time.UnixMilli(1)),
	}}
	g := gatewayWithFake(fake)

	logBytes := captureSlog(t, func() {
		body := `{"text":"` + secretText + `"}`
		req := httptest.NewRequest(http.MethodPost, "/rooms/RG000000004/messages", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearerToken) // defense in depth
		req = withUserContext(t, req, "1")

		mux := http.NewServeMux()
		mux.HandleFunc("POST /rooms/{id}/messages", g.handleRoomSendMessage)
		mux.ServeHTTP(httptest.NewRecorder(), req)
	})

	logs := string(logBytes)
	if strings.Contains(logs, secretText) {
		t.Errorf("text body leaked into logs: %s", logs)
	}
	if strings.Contains(logs, bearerToken) {
		t.Errorf("bearer token leaked into logs: %s", logs)
	}
	if !strings.Contains(logs, `"text_len":`+strconv.Itoa(len(secretText))) {
		t.Errorf("expected text_len=%d in logs, got: %s", len(secretText), logs)
	}
}

// ---- mux-level path-not-found regression test --------------------------

// Go's ServeMux normalises double-slash paths and returns a 307 redirect
// to the cleaned path. The important invariants for this story are
// (a) the handler is never invoked with an empty {id} capture, and
// (b) the response is not a 200 from one of our handlers.
func TestRoomMux_EmptyIDSegmentNeverReachesHandler(t *testing.T) {
	fake := &fakeUserGrainCaller{
		joinResp:  &userpb.JoinRoomResponse{},
		leaveResp: &userpb.LeaveRoomResponse{},
	}
	g := gatewayWithFake(fake)
	handler := g.RegisterRoutes()

	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		path := "/rooms//membership"
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		// Either a redirect (Go normalises //) or a 404; both are fine.
		// The forbidden case is a 200, which would mean the handler
		// dispatched with an empty {id} capture.
		if rec.Code != http.StatusTemporaryRedirect && rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusNotFound {
			t.Errorf("%s %q: unexpected status %d (body=%s)", method, path, rec.Code, rec.Body.String())
		}
		if fake.calls != 0 {
			t.Errorf("%s %q: handler invoked despite empty {id} segment (calls=%d)", method, path, fake.calls)
		}
	}
}
