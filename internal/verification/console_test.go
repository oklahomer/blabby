package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"log/slog"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestConsoleSender_EmitsGreppableRecord(t *testing.T) {
	var buf bytes.Buffer
	sender := &consoleSender{logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	to, err := domain.NewMailAddress("alice@example.com")
	if err != nil {
		t.Fatalf("NewMailAddress: %v", err)
	}
	pin, err := ParsePIN("482915")
	if err != nil {
		t.Fatalf("ParsePIN: %v", err)
	}
	if err := sender.Send(context.Background(), to, pin, 10*time.Minute); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("decode log record: %v (raw=%s)", err, buf.String())
	}
	if rec["msg"] != consoleLogEvent {
		t.Errorf("msg = %v, want %q", rec["msg"], consoleLogEvent)
	}
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN (prominent)", rec["level"])
	}
	if rec["mail_address"] != "alice@example.com" {
		t.Errorf("mail_address = %v", rec["mail_address"])
	}
	if rec["pin"] != "482915" {
		t.Errorf("pin = %v, want 482915", rec["pin"])
	}
	if rec["expires_in"] != "10m0s" {
		t.Errorf("expires_in = %v, want 10m0s", rec["expires_in"])
	}
}

func TestConsoleSender_RejectsZeroPIN(t *testing.T) {
	var buf bytes.Buffer
	sender := &consoleSender{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	to, _ := domain.NewMailAddress("alice@example.com")

	if err := sender.Send(context.Background(), to, PIN{}, time.Minute); err == nil {
		t.Fatal("Send(zero PIN) = nil, want a validation error")
	}
	if buf.Len() != 0 {
		t.Errorf("a zero-value PIN was logged:\n%s", buf.String())
	}
}
