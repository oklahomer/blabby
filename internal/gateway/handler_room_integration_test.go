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

	roompb "github.com/oklahomer/blabby/gen/room"
	"github.com/oklahomer/blabby/internal/auth"
	"github.com/oklahomer/blabby/internal/gateway"
	"github.com/oklahomer/blabby/internal/grain/user"
	"github.com/oklahomer/blabby/internal/ids"
	clustertest "github.com/oklahomer/blabby/internal/testutil/cluster"
)

// integrationAuth is an Authenticator that issues a deterministic token
// for one user and validates that token by string equality.
type integrationAuth struct {
	userID string
	token  string
}

func (a *integrationAuth) Authenticate(_ context.Context, _ auth.AuthParams) (*auth.Result, error) {
	uid, err := ids.NewUserID(a.userID)
	if err != nil {
		return nil, err
	}
	return &auth.Result{UserID: uid, Token: a.token}, nil
}

func (a *integrationAuth) ValidateToken(_ context.Context, token string) (*auth.Claims, error) {
	if token != a.token {
		return nil, auth.ErrTokenInvalid
	}
	uid, err := ids.NewUserID(a.userID)
	if err != nil {
		return nil, auth.ErrTokenInvalid
	}
	return &auth.Claims{UserID: uid}, nil
}

// stubRoomGrain stands in for the production Room grain so the User
// grain's outgoing Join/Leave/PostMessage RPCs return immediately
// without fanning back into the User grain — that fan-out would
// deadlock against the in-flight gateway request on a single-member
// cluster. Mirrors the pattern in internal/grain/user/integration_test.go.
type stubRoomGrain struct {
	postCount *int64
	postTime  time.Time
}

func (s *stubRoomGrain) Init(cluster.GrainContext)           {}
func (s *stubRoomGrain) Terminate(cluster.GrainContext)      {}
func (s *stubRoomGrain) ReceiveDefault(cluster.GrainContext) {}

func (s *stubRoomGrain) Join(*roompb.JoinRequest, cluster.GrainContext) (*roompb.JoinResponse, error) {
	return &roompb.JoinResponse{}, nil
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
	const userID = "alice-room-int"
	const roomID = "general"
	const bearer = "integration-token-room"
	stubPostTime := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	var postCount int64
	roomKind := roompb.NewRoomGrainKind(func() roompb.RoomGrain {
		return &stubRoomGrain{postCount: &postCount, postTime: stubPostTime}
	}, time.Minute)
	userKind := user.NewKind()

	c := clustertest.Start(t, roomKind, userKind)

	g := gateway.NewGateway(&integrationAuth{userID: userID, token: bearer}, c, c.ActorSystem.Root)
	srv := httptest.NewServer(g.RegisterRoutes())
	t.Cleanup(srv.Close)

	authHeader := "Bearer " + bearer

	// 1. Join "general"
	if rec := doPOST(t, srv.URL+"/rooms/"+roomID+"/join", "", "", authHeader); rec.status != http.StatusOK {
		t.Fatalf("join: status = %d (body=%s)", rec.status, rec.body)
	}

	// 2. Confirm via /rooms/joined that membership landed.
	rec := doGET(t, srv.URL+"/rooms/joined", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("/rooms/joined after join: status = %d (body=%s)", rec.status, rec.body)
	}
	var joined struct {
		RoomIDs []string `json:"room_ids"`
	}
	if err := json.Unmarshal([]byte(rec.body), &joined); err != nil {
		t.Fatalf("decode joined: %v", err)
	}
	if len(joined.RoomIDs) != 1 || joined.RoomIDs[0] != roomID {
		t.Errorf("joined rooms after join: got %v, want [%q]", joined.RoomIDs, roomID)
	}

	// 3. Send a message and confirm a non-zero timestamp came back.
	rec = doPOST(t, srv.URL+"/rooms/"+roomID+"/messages",
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

	// 4. Leave the room and confirm membership is empty afterwards.
	if rec := doPOST(t, srv.URL+"/rooms/"+roomID+"/leave", "", "", authHeader); rec.status != http.StatusOK {
		t.Fatalf("leave: status = %d (body=%s)", rec.status, rec.body)
	}
	rec = doGET(t, srv.URL+"/rooms/joined", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("/rooms/joined after leave: status = %d", rec.status)
	}
	if err := json.Unmarshal([]byte(rec.body), &joined); err != nil {
		t.Fatalf("decode joined-after-leave: %v", err)
	}
	if len(joined.RoomIDs) != 0 {
		t.Errorf("joined rooms after leave: got %v, want []", joined.RoomIDs)
	}

	// 5. /rooms returns the static catalogue.
	rec = doGET(t, srv.URL+"/rooms", authHeader)
	if rec.status != http.StatusOK {
		t.Fatalf("/rooms: status = %d", rec.status)
	}
	if !strings.Contains(rec.body, `"id":"general"`) {
		t.Errorf("/rooms body missing default room: %s", rec.body)
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
