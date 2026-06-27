package verification

import (
	"strings"
	"testing"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestConfigFromEnv_ConsoleDefault(t *testing.T) {
	// Empty delivery selects console with no further configuration required.
	t.Setenv(EnvDelivery, "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Mode != ModeConsole {
		t.Errorf("Mode = %q, want %q", cfg.Mode, ModeConsole)
	}
}

func TestConfigFromEnv_ConsoleExplicit_IgnoresSMTP(t *testing.T) {
	// Stray SMTP settings are irrelevant in console mode and must not trip fail-fast.
	t.Setenv(EnvDelivery, "Console")
	t.Setenv(EnvMailFrom, "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Mode != ModeConsole {
		t.Errorf("Mode = %q, want console", cfg.Mode)
	}
}

func TestConfigFromEnv_SMTP_Valid(t *testing.T) {
	t.Setenv(EnvDelivery, "smtp")
	t.Setenv(EnvSMTPAddr, "mailpit:1025")
	t.Setenv(EnvSMTPUsername, "user")
	t.Setenv(EnvSMTPPassword, "pass")
	t.Setenv(EnvMailFrom, "dev@blabby.local")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.Mode != ModeSMTP || cfg.SMTPAddr != "mailpit:1025" || cfg.SMTPUsername != "user" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.MailFrom.String() != "dev@blabby.local" {
		t.Errorf("MailFrom = %q, want dev@blabby.local", cfg.MailFrom.String())
	}
}

func TestConfigFromEnv_SMTP_FailFast(t *testing.T) {
	tests := []struct {
		name      string
		addr      string
		from      string
		wantInErr string
	}{
		{name: "missing addr", addr: "", from: "dev@blabby.local", wantInErr: EnvSMTPAddr},
		{name: "addr without port", addr: "localhost", from: "dev@blabby.local", wantInErr: EnvSMTPAddr},
		{name: "missing from", addr: "mailpit:1025", from: "", wantInErr: EnvMailFrom},
		{name: "invalid from", addr: "mailpit:1025", from: "not-an-email", wantInErr: EnvMailFrom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(EnvDelivery, "smtp")
			t.Setenv(EnvSMTPAddr, tc.addr)
			t.Setenv(EnvMailFrom, tc.from)
			_, err := ConfigFromEnv()
			if err == nil {
				t.Fatal("ConfigFromEnv: want a fail-fast error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not mention %q", err, tc.wantInErr)
			}
		})
	}
}

func TestConfigFromEnv_UnknownMode(t *testing.T) {
	t.Setenv(EnvDelivery, "carrier-pigeon")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("ConfigFromEnv: want an error for an unknown mode")
	}
}

func TestNewSender_Selection(t *testing.T) {
	console, err := NewSender(Config{Mode: ModeConsole})
	if err != nil {
		t.Fatalf("NewSender(console): %v", err)
	}
	if _, ok := console.(*consoleSender); !ok {
		t.Errorf("console sender = %T, want *consoleSender", console)
	}
	zero, err := NewSender(Config{})
	if err != nil {
		t.Fatalf("NewSender(zero config): %v", err)
	}
	if _, ok := zero.(*consoleSender); !ok {
		t.Errorf("zero config sender = %T, want *consoleSender", zero)
	}

	from, err := domain.NewMailAddress("dev@blabby.local")
	if err != nil {
		t.Fatalf("NewMailAddress: %v", err)
	}
	delivery, err := NewSender(Config{Mode: ModeSMTP, SMTPAddr: "mailpit:1025", MailFrom: from})
	if err != nil {
		t.Fatalf("NewSender(smtp): %v", err)
	}
	if _, ok := delivery.(*smtpSender); !ok {
		t.Errorf("smtp sender = %T, want *smtpSender", delivery)
	}

	// A smtp config missing its required fields is rejected, not silently built.
	// The returned Sender must be a true nil interface, not a typed-nil wrapping a
	// (*smtpSender)(nil), so a caller checking the value isn't misled.
	if s, err := NewSender(Config{Mode: ModeSMTP}); err == nil || s != nil {
		t.Errorf("NewSender(smtp, empty) = (%v, %v), want (nil, error)", s, err)
	}
	if _, err := NewSender(Config{Mode: ModeSMTP, SMTPAddr: "localhost", MailFrom: from}); err == nil {
		t.Error("NewSender(smtp, addr without port) = nil error, want rejection")
	}
}
