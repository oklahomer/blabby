package verification

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
)

// Sender delivers a verification PIN to an account's email address.
// Implementations are safe for concurrent use. expiresIn is the challenge's
// remaining lifetime, so a delivery can tell the recipient how long the PIN lasts.
type Sender interface {
	Send(ctx context.Context, to domain.MailAddress, pin PIN, expiresIn time.Duration) error
}

// Mode selects how a verification PIN is delivered.
type Mode string

const (
	// ModeConsole logs the PIN to the gateway log and sends no email. It is the
	// default: zero configuration, no owned domain, nothing leaves the process.
	ModeConsole Mode = "console"
	// ModeSMTP sends the PIN as an email through an SMTP relay.
	ModeSMTP Mode = "smtp"
)

// Environment variables that configure delivery.
const (
	EnvDelivery     = "BLABBY_VERIFICATION_DELIVERY"
	EnvSMTPAddr     = "BLABBY_SMTP_ADDR"
	EnvSMTPUsername = "BLABBY_SMTP_USERNAME"
	EnvSMTPPassword = "BLABBY_SMTP_PASSWORD"
	EnvMailFrom     = "BLABBY_MAIL_FROM"
)

// Config is the parsed delivery configuration. In console mode only Mode is set;
// the SMTP fields are populated and validated only in smtp mode.
type Config struct {
	Mode         Mode
	SMTPAddr     string
	SMTPUsername string
	SMTPPassword string
	MailFrom     domain.MailAddress
}

// ConfigFromEnv parses the delivery configuration from the environment. The
// default (unset or "console") needs nothing more. In "smtp" mode it requires
// BLABBY_SMTP_ADDR and a valid BLABBY_MAIL_FROM and fails fast otherwise — there
// is no fake default sender, so a misconfigured smtp deployment must not start.
func ConfigFromEnv() (Config, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(os.Getenv(EnvDelivery))))
	switch mode {
	case "", ModeConsole:
		return Config{Mode: ModeConsole}, nil
	case ModeSMTP:
		addr := strings.TrimSpace(os.Getenv(EnvSMTPAddr))
		if err := validateSMTPAddr(addr); err != nil {
			return Config{}, err
		}
		rawFrom := strings.TrimSpace(os.Getenv(EnvMailFrom))
		if rawFrom == "" {
			return Config{}, fmt.Errorf("verification: %s is required when %s=smtp", EnvMailFrom, EnvDelivery)
		}
		from, err := domain.NewMailAddress(rawFrom)
		if err != nil {
			return Config{}, fmt.Errorf("verification: %s is not a valid email: %w", EnvMailFrom, err)
		}
		return Config{
			Mode:         ModeSMTP,
			SMTPAddr:     addr,
			SMTPUsername: os.Getenv(EnvSMTPUsername),
			SMTPPassword: os.Getenv(EnvSMTPPassword),
			MailFrom:     from,
		}, nil
	default:
		return Config{}, fmt.Errorf("verification: unknown %s=%q (want %q or %q)", EnvDelivery, mode, ModeConsole, ModeSMTP)
	}
}

// NewSender builds the Sender selected by cfg. Console needs no further input;
// smtp requires the addr and from that ConfigFromEnv already validated (re-checked
// here so a hand-built Config cannot produce a sender that fails only at send).
func NewSender(cfg Config) (Sender, error) {
	switch cfg.Mode {
	case "", ModeConsole:
		return newConsoleSender(), nil
	case ModeSMTP:
		// Assign and return an explicit nil on error rather than passing the
		// concrete-typed result through: a `return newSMTPSender(cfg)` would wrap a
		// (*smtpSender)(nil) into a non-nil Sender interface on the error path.
		s, err := newSMTPSender(cfg)
		if err != nil {
			return nil, err
		}
		return s, nil
	default:
		return nil, fmt.Errorf("verification: unknown delivery mode %q", cfg.Mode)
	}
}

func validateSMTPConfig(cfg Config) error {
	if err := validateSMTPAddr(cfg.SMTPAddr); err != nil {
		return err
	}
	if cfg.MailFrom == (domain.MailAddress{}) {
		return fmt.Errorf("verification: smtp delivery requires %s", EnvMailFrom)
	}
	return nil
}

func validateSMTPAddr(addr string) error {
	if addr == "" {
		return fmt.Errorf("verification: smtp delivery requires %s", EnvSMTPAddr)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("verification: %s must be host:port: %w", EnvSMTPAddr, err)
	}
	return nil
}
