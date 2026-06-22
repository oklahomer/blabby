package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asynkron/protoactor-go/cluster"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/oklahomer/blabby/gen/common"
	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/gateway"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/id"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// integrationAuth is an Authenticator that issues a deterministic token
// for one user and validates that token by string equality.
type integrationAuth struct {
	userID string
	token  string
}

func (a *integrationAuth) Authenticate(_ context.Context, _ auth.AuthParams) (*auth.Result, error) {
	uid, err := id.ParseUserID(a.userID)
	if err != nil {
		return nil, err
	}
	return &auth.Result{UserID: uid, Token: a.token}, nil
}

func (a *integrationAuth) ValidateToken(_ context.Context, token string) (*auth.Claims, error) {
	if token != a.token {
		return nil, auth.ErrTokenInvalid
	}
	uid, err := id.ParseUserID(a.userID)
	if err != nil {
		return nil, auth.ErrTokenInvalid
	}
	return &auth.Claims{UserID: uid}, nil
}

// stubRoomGrain stands in for the production Room grain so this test
// exercises the gateway → User grain HTTP path in isolation, without the
// Room grain's member fan-out. (Real Room→User fan-out, including the
// acting user's self-echo, is covered by the room package's fan-out
// integration test.) Mirrors the pattern in
// internal/grain/user/integration_test.go.
type stubRoomGrain struct {
	postCount *int64
	postTime  time.Time
}

func (s *stubRoomGrain) Init(cluster.GrainContext)           {}
func (s *stubRoomGrain) Terminate(cluster.GrainContext)      {}
func (s *stubRoomGrain) ReceiveDefault(cluster.GrainContext) {}

func (s *stubRoomGrain) Join(*roompb.JoinRequest, cluster.GrainContext) (*roompb.JoinResponse, error) {
	// A loaded Room grain returns its RoomRef so the User grain caches it; the
	// public code renders back as RG000000004 on /rooms/joined.
	return &roompb.JoinResponse{Room: &commonpb.RoomRef{
		RoomId:     "4",
		PublicCode: "G000000004",
		Name:       "General",
		Status:     "active",
	}}, nil
}
func (s *stubRoomGrain) Leave(*roompb.LeaveRequest, cluster.GrainContext) (*roompb.LeaveResponse, error) {
	return &roompb.LeaveResponse{}, nil
}
func (s *stubRoomGrain) PostMessage(*roompb.PostMessageRequest, cluster.GrainContext) (*roompb.PostMessageResponse, error) {
	atomic.AddInt64(s.postCount, 1)
	return &roompb.PostMessageResponse{Timestamp: timestamppb.New(s.postTime)}, nil
}

// TestGateway_RoomEndpoints_Integration drives the join → joined →
// send → leave → joined flow over HTTP through a real Gateway, real
// User grain, and a stub Room grain in an in-process cluster. The
// stub Room grain isolates this test from Room → User fan-out, which
// would deadlock on a single-member cluster.
func TestGateway_RoomEndpoints_Integration(t *testing.T) {
	const userID = "1"
	const roomCode = "RG000000004" // resolves to internal room id 4 via the stub directory
	const bearer = "integration-token-room"
	stubPostTime := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	var postCount int64
	roomKind := roompb.NewRoomGrainKind(func() roompb.RoomGrain {
		return &stubRoomGrain{postCount: &postCount, postTime: stubPostTime}
	}, time.Minute)
	userKind := user.NewKind(nil)

	c := clustertest.Start(t, roomKind, userKind)

	g := gateway.NewGateway(&integrationAuth{userID: userID, token: bearer}, newStubRoomDirectory(), c, c.ActorSystem.Root)
	srv := httptest.NewServer(g.RegisterRoutes())
	t.Cleanup(srv.Close)

	authHeader := "Bearer " + bearer

	// 1. Ensure membership in "general" twice; both requests are successful.
	if rec := doMembership(t, http.MethodPut, srv.URL+"/rooms/"+roomCode+"/membership", authHeader); rec.status != http.StatusOK {
		t.Fatalf("join: status = %d (body=%s)", rec.status, rec.body)
	}
	if rec := doMembership(t, http.MethodPut, srv.URL+"/rooms/"+roomCode+"/membership", authHeader); rec.status != http.StatusOK {
		t.Fatalf("repeated join: status = %d (body=%s)", rec.status, rec.body)
	}

	// 2. Confirm via /rooms/joined that membership landed.
	rec := doGET(t, srv.URL+"/rooms/joined", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("/rooms/joined after join: status = %d (body=%s)", rec.status, rec.body)
	}
	var joined struct {
		Rooms []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rooms"`
	}
	if err := json.Unmarshal([]byte(rec.body), &joined); err != nil {
		t.Fatalf("decode joined: %v", err)
	}
	if len(joined.Rooms) != 1 || joined.Rooms[0].ID != roomCode {
		t.Errorf("joined rooms after join: got %+v, want [%q]", joined.Rooms, roomCode)
	}

	// 3. Send a message and confirm a non-zero timestamp came back.
	rec = doPOST(t, srv.URL+"/rooms/"+roomCode+"/messages",
		`{"text":"integration"}`, "application/json", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("send: status = %d (body=%s)", rec.status, rec.body)
	}
	var sent struct {
		Success   bool  `json:"success"`
		Timestamp int64 `json:"timestamp"`
	}
	if err := json.Unmarshal([]byte(rec.body), &sent); err != nil {
		t.Fatalf("decode send: %v", err)
	}
	if !sent.Success || sent.Timestamp != stubPostTime.UnixMilli() {
		t.Errorf("send response: got %+v, want success=true and timestamp=%d",
			sent, stubPostTime.UnixMilli())
	}
	if got := atomic.LoadInt64(&postCount); got != 1 {
		t.Errorf("stub Room.PostMessage calls: got %d, want 1", got)
	}

	// 4. Ensure membership is absent twice and confirm the list stays empty.
	if rec := doMembership(t, http.MethodDelete, srv.URL+"/rooms/"+roomCode+"/membership", authHeader); rec.status != http.StatusOK {
		t.Fatalf("leave: status = %d (body=%s)", rec.status, rec.body)
	}
	if rec := doMembership(t, http.MethodDelete, srv.URL+"/rooms/"+roomCode+"/membership", authHeader); rec.status != http.StatusOK {
		t.Fatalf("repeated leave: status = %d (body=%s)", rec.status, rec.body)
	}
	rec = doGET(t, srv.URL+"/rooms/joined", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("/rooms/joined after leave: status = %d", rec.status)
	}
	if err := json.Unmarshal([]byte(rec.body), &joined); err != nil {
		t.Fatalf("decode joined-after-leave: %v", err)
	}
	if len(joined.Rooms) != 0 {
		t.Errorf("joined rooms after leave: got %+v, want []", joined.Rooms)
	}

	// 5. /rooms returns the active-room catalogue (directory-backed; here the
	// stub directory) as R… descriptors.
	rec = doGET(t, srv.URL+"/rooms", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("/rooms: status = %d", rec.status)
	}
	if !strings.Contains(rec.body, `"id":"RG000000004"`) {
		t.Errorf("/rooms body missing seeded room: %s", rec.body)
	}
}

type httpRec struct {
	status int
	body   string
}

func doGET(t *testing.T, url, authz string) httpRec {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	req.Header.Set("Authorization", authz)
	return doRequest(t, req)
}

func doPOST(t *testing.T, url, body, contentType, authz string) httpRec {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, url, reader)
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", authz)
	return doRequest(t, req)
}

func doMembership(t *testing.T, method, url, authz string) httpRec {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	req.Header.Set("Authorization", authz)
	return doRequest(t, req)
}

func doRequest(t *testing.T, req *http.Request) httpRec {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP %s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // close error on a finished HTTP response body is not actionable
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return httpRec{status: resp.StatusCode, body: string(body)}
}
