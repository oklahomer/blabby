package verification

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
)

func TestBuildMessage(t *testing.T) {
	from, _ := domain.NewMailAddress("dev@blabby.local")
	to, _ := domain.NewMailAddress("alice@example.com")
	pin, _ := ParsePIN("482915")

	msg := string(buildMessage(from, to, pin, 10*time.Minute))

	for _, want := range []string{
		"From: dev@blabby.local\r\n",
		"To: alice@example.com\r\n",
		"Subject: Your blabby verification code\r\n",
		"482915",
		"10m0s",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	// Headers must be separated from the body by a blank line.
	if !strings.Contains(msg, "\r\n\r\n") {
		t.Errorf("message has no header/body separator:\n%s", msg)
	}
}

func TestSMTPSender_RejectsZeroPIN(t *testing.T) {
	from, _ := domain.NewMailAddress("dev@blabby.local")
	to, _ := domain.NewMailAddress("alice@example.com")
	s, err := newSMTPSender(Config{Mode: ModeSMTP, SMTPAddr: "mailpit:1025", MailFrom: from})
	if err != nil {
		t.Fatalf("newSMTPSender: %v", err)
	}

	// The guard fires before any network call, so a zero PIN never reaches the relay.
	if err := s.Send(context.Background(), to, PIN{}, time.Minute); err == nil {
		t.Fatal("Send(zero PIN) = nil, want a validation error")
	}
}

func TestNewSMTPSender_MapsConfig(t *testing.T) {
	from, _ := domain.NewMailAddress("dev@blabby.local")
	s, err := newSMTPSender(Config{
		Mode: ModeSMTP, SMTPAddr: "mailpit:1025", SMTPUsername: "u", SMTPPassword: "p", MailFrom: from,
	})
	if err != nil {
		t.Fatalf("newSMTPSender: %v", err)
	}
	if s.addr != "mailpit:1025" || s.username != "u" || s.password != "p" || s.from.String() != "dev@blabby.local" {
		t.Errorf("sender = %+v", s)
	}
}

func TestNewSMTPSender_RejectsInvalidConfig(t *testing.T) {
	from, _ := domain.NewMailAddress("dev@blabby.local")
	if _, err := newSMTPSender(Config{Mode: ModeSMTP, SMTPAddr: "localhost", MailFrom: from}); err == nil {
		t.Fatal("newSMTPSender(addr without port) = nil error, want rejection")
	}
	if _, err := newSMTPSender(Config{Mode: ModeSMTP, SMTPAddr: "mailpit:1025"}); err == nil {
		t.Fatal("newSMTPSender(missing from) = nil error, want rejection")
	}
}
