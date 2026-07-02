package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oklahomer/blabby/internal/errcode"
)

type fakeMaintenanceTrigger struct {
	accepted bool
	err      error
	called   bool
}

func (f *fakeMaintenanceTrigger) TriggerPendingAccountGC(context.Context) (bool, error) {
	f.called = true
	return f.accepted, f.err
}

func gatewayWithMaintenance(m MaintenanceTrigger) *Gateway {
	return NewGateway(Deps{Maintenance: m})
}

func TestHandlePendingAccountGC(t *testing.T) {
	tests := []struct {
		name          string
		trigger       *fakeMaintenanceTrigger
		wantStatus    int
		wantAccepted  bool
		wantReason    string
		wantErrorCode errcode.Code // 0 means a PendingAccountGCResponse body
	}{
		{"accepted returns 202", &fakeMaintenanceTrigger{accepted: true}, http.StatusAccepted, true, "", 0},
		{"already running returns 200", &fakeMaintenanceTrigger{accepted: false}, http.StatusOK, false, "already_running", 0},
		{"trigger failure returns 503", &fakeMaintenanceTrigger{err: errors.New("cluster down")}, http.StatusServiceUnavailable, false, "", errcode.ServiceUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := gatewayWithMaintenance(tc.trigger)
			req := httptest.NewRequest(http.MethodPost, "/internal/jobs/pending-account-gc", nil)
			rec := httptest.NewRecorder()
			g.RegisterInternalRoutes().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !tc.trigger.called {
				t.Error("trigger was not called")
			}
			if tc.wantErrorCode != 0 {
				resp := decodeErrorResponse(t, rec.Body)
				if resp.Error.Code != tc.wantErrorCode {
					t.Errorf("error.code: got %d, want %d", resp.Error.Code, tc.wantErrorCode)
				}
				return
			}
			var resp PendingAccountGCResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Accepted != tc.wantAccepted || resp.Reason != tc.wantReason {
				t.Errorf("response = %+v, want accepted=%v reason=%q", resp, tc.wantAccepted, tc.wantReason)
			}
		})
	}
}

func TestHandlePendingAccountGC_WrongMethodReturns405(t *testing.T) {
	g := gatewayWithMaintenance(&fakeMaintenanceTrigger{})
	req := httptest.NewRequest(http.MethodGet, "/internal/jobs/pending-account-gc", nil)
	rec := httptest.NewRecorder()
	g.RegisterInternalRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d, want 405", rec.Code)
	}
}

func TestInternalEndpointIsNotOnPublicListener(t *testing.T) {
	trigger := &fakeMaintenanceTrigger{accepted: true}
	g := gatewayWithMaintenance(trigger)

	req := httptest.NewRequest(http.MethodPost, "/internal/jobs/pending-account-gc", nil)
	rec := httptest.NewRecorder()
	g.RegisterRoutes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("internal endpoint reachable on the public listener: status %d, want 404", rec.Code)
	}
	if trigger.called {
		t.Error("public listener routed to the maintenance trigger; it must not")
	}
}
