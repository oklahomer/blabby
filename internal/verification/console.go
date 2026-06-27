package verification

import (
	"context"
	"log/slog"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
)

// consoleLogEvent is the deliberately greppable slog message a demo player watches
// for; it is part of the contract, so tests pin it.
const consoleLogEvent = "verification.console_pin"

// consoleSender logs the verification PIN to the gateway log and sends no email.
// It is the zero-config default for the no-owned-domain demo: the registrant's
// address is only an identifier, so any syntactically valid email works and the
// player reads the PIN straight from the log. Logging a secret is by design here
// (dev-only) and never happens on the SMTP path.
type consoleSender struct {
	logger *slog.Logger
}

// newConsoleSender returns a consoleSender that writes to the default slog logger.
func newConsoleSender() *consoleSender {
	return &consoleSender{logger: slog.Default()}
}

// Send emits the prominent verification.console_pin record and returns nil — there
// is no external delivery to fail.
func (s *consoleSender) Send(_ context.Context, to domain.MailAddress, pin PIN, expiresIn time.Duration) error {
	if err := pin.validate(); err != nil {
		return err
	}
	s.logger.Warn(consoleLogEvent,
		"mail_address", to.String(),
		"pin", pin.String(),
		"expires_in", expiresIn.String(),
		"note", "console delivery — no email sent",
	)
	return nil
}
