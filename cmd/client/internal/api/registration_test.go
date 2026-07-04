package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegisterCmdSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/users" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if req["mail_address"] != "dana@example.com" || req["handle"] != "Dana_99" || req["password"] != "a-long-passphrase" {
			t.Errorf("got fields %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"public_code":"UA1B2C3D4E5"}`))
	}))
	defer srv.Close()

	msg := RegisterCmd(srv.Client(), srv.URL, "dana@example.com", "Dana_99", "a-long-passphrase", 2*time.Second)()
	got, ok := msg.(RegisterSucceeded)
	if !ok {
		t.Fatalf("expected RegisterSucceeded, got %T: %#v", msg, msg)
	}
	if got.Email != "dana@example.com" {
		t.Fatalf("got email %q", got.Email)
	}
}

func TestRegisterCmdRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
			Code: 6002, Status: "HANDLE_ALREADY_TAKEN", Message: "handle is already taken",
		}})
	}))
	defer srv.Close()

	msg := RegisterCmd(srv.Client(), srv.URL, "dana@example.com", "Dana_99", "a-long-passphrase", 2*time.Second)()
	got, ok := msg.(RegisterRejected)
	if !ok {
		t.Fatalf("expected RegisterRejected, got %T", msg)
	}
	if got.Status != "HANDLE_ALREADY_TAKEN" || got.HTTPStatus != http.StatusConflict {
		t.Fatalf("got %+v", got)
	}
}

func TestRegisterCmdTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	msg := RegisterCmd(&http.Client{}, addr, "dana@example.com", "Dana_99", "a-long-passphrase", 500*time.Millisecond)()
	if _, ok := msg.(RegisterTransportError); !ok {
		t.Fatalf("expected RegisterTransportError, got %T", msg)
	}
}

func TestRegisterCmdPasswordNeverLeaksToErrorPath(t *testing.T) {
	const secret = "a-long-passphrase"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`malformed`))
	}))
	defer srv.Close()

	msg := RegisterCmd(srv.Client(), srv.URL, "dana@example.com", "Dana_99", secret, time.Second)()
	if rej, ok := msg.(RegisterRejected); ok && strings.Contains(rej.Message, secret) {
		t.Fatalf("RegisterRejected message leaked password: %q", rej.Message)
	}
	if te, ok := msg.(RegisterTransportError); ok && strings.Contains(te.Err.Error(), secret) {
		t.Fatalf("RegisterTransportError leaked password: %q", te.Err)
	}
}

func TestVerifyEmailCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/users/verifications" {
				http.Error(w, "wrong route", http.StatusNotFound)
				return
			}
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if req["mail_address"] != "dana@example.com" || req["pin"] != "482910" {
				t.Errorf("got fields %#v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		}))
		defer srv.Close()

		msg := VerifyEmailCmd(srv.Client(), srv.URL, "dana@example.com", "482910", 2*time.Second)()
		got, ok := msg.(VerifySucceeded)
		if !ok {
			t.Fatalf("expected VerifySucceeded, got %T", msg)
		}
		if got.Email != "dana@example.com" {
			t.Fatalf("got email %q", got.Email)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
				Code: 6003, Status: "VERIFICATION_INVALID", Message: "verification failed",
			}})
		}))
		defer srv.Close()

		msg := VerifyEmailCmd(srv.Client(), srv.URL, "dana@example.com", "000000", 2*time.Second)()
		got, ok := msg.(VerifyRejected)
		if !ok {
			t.Fatalf("expected VerifyRejected, got %T", msg)
		}
		if got.Status != "VERIFICATION_INVALID" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		msg := VerifyEmailCmd(&http.Client{}, addr, "dana@example.com", "482910", 500*time.Millisecond)()
		if _, ok := msg.(VerifyTransportError); !ok {
			t.Fatalf("expected VerifyTransportError, got %T", msg)
		}
	})
}

func TestResendVerificationCmd(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/users/verifications/resend" {
				http.Error(w, "wrong route", http.StatusNotFound)
				return
			}
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode body: %v", err)
			}
			if req["mail_address"] != "dana@example.com" {
				t.Errorf("got fields %#v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		}))
		defer srv.Close()

		msg := ResendVerificationCmd(srv.Client(), srv.URL, "dana@example.com", 2*time.Second)()
		if _, ok := msg.(ResendSucceeded); !ok {
			t.Fatalf("expected ResendSucceeded, got %T", msg)
		}
	})

	t.Run("rate limited", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(ErrorEnvelope{Error: ErrorDetail{
				Code: 3002, Status: "VERIFICATION_RATE_LIMITED", Message: "too many attempts",
			}})
		}))
		defer srv.Close()

		msg := ResendVerificationCmd(srv.Client(), srv.URL, "dana@example.com", 2*time.Second)()
		got, ok := msg.(ResendRejected)
		if !ok {
			t.Fatalf("expected ResendRejected, got %T", msg)
		}
		if got.Status != "VERIFICATION_RATE_LIMITED" || got.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		msg := ResendVerificationCmd(&http.Client{}, addr, "dana@example.com", 500*time.Millisecond)()
		if _, ok := msg.(ResendTransportError); !ok {
			t.Fatalf("expected ResendTransportError, got %T", msg)
		}
	})
}
