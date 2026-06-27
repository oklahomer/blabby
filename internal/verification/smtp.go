package verification

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/smtp"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
)

// smtpSender delivers the PIN as an email through an SMTP relay. It is opt-in
// (BLABBY_VERIFICATION_DELIVERY=smtp) and never logs the PIN.
type smtpSender struct {
	addr     string
	username string
	password string
	from     domain.MailAddress
}

// newSMTPSender builds an smtpSender from cfg after validating the smtp-only
// required fields. Most callers should use NewSender so delivery mode selection
// stays centralized.
func newSMTPSender(cfg Config) (*smtpSender, error) {
	if err := validateSMTPConfig(cfg); err != nil {
		return nil, err
	}
	return &smtpSender{
		addr:     cfg.SMTPAddr,
		username: cfg.SMTPUsername,
		password: cfg.SMTPPassword,
		from:     cfg.MailFrom,
	}, nil
}

// Send delivers the PIN email. net/smtp.SendMail does not honor a context, so ctx
// only short-circuits an already-cancelled call; the gateway bounds the overall
// request elsewhere. Authentication is used only when a username is configured —
// Mailpit and other dev relays accept unauthenticated mail.
func (s *smtpSender) Send(ctx context.Context, to domain.MailAddress, pin PIN, expiresIn time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := pin.validate(); err != nil {
		return err
	}
	var auth smtp.Auth
	if s.username != "" {
		host, _, err := net.SplitHostPort(s.addr)
		if err != nil {
			return fmt.Errorf("verification: smtp addr %q: %w", s.addr, err)
		}
		auth = smtp.PlainAuth("", s.username, s.password, host)
	}
	msg := buildMessage(s.from, to, pin, expiresIn)
	if err := smtp.SendMail(s.addr, auth, s.from.String(), []string{to.String()}, msg); err != nil {
		return fmt.Errorf("verification: smtp send: %w", err)
	}
	return nil
}

// buildMessage renders a minimal RFC 5322 message carrying the PIN. The body
// states the expiry so the recipient knows the validity window.
func buildMessage(from, to domain.MailAddress, pin PIN, expiresIn time.Duration) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from.String())
	fmt.Fprintf(&b, "To: %s\r\n", to.String())
	fmt.Fprintf(&b, "Subject: Your blabby verification code\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	fmt.Fprintf(&b, "Your blabby verification code is %s.\r\n", pin.String())
	fmt.Fprintf(&b, "It expires in %s.\r\n", expiresIn.String())
	return b.Bytes()
}
