package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/persistence"
	"github.com/oklahomer/blabby/internal/persistence/verifyrepo"
	"github.com/oklahomer/blabby/internal/verification"
)

// aliceMail is the parsed email the verification fakes are keyed on.
func aliceMail(t *testing.T) domain.MailAddress {
	t.Helper()
	mail, err := domain.NewMailAddress("alice@example.com")
	if err != nil {
		t.Fatalf("NewMailAddress: %v", err)
	}
	return mail
}

// pinChallenge builds a verification row whose pin_hash matches the returned raw
// PIN, with the given attempt count and expiry, so a test can submit the right or a
// wrong PIN against it.
func pinChallenge(t *testing.T, attempts int, expiresAt time.Time) (verifyrepo.Verification, string) {
	t.Helper()
	pin, err := verification.NewPIN()
	if err != nil {
		t.Fatalf("NewPIN: %v", err)
	}
	hash, err := pin.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	uid := pendingAlice(t).ID
	return verifyrepo.Verification{UserID: uid, PinHash: hash, ExpiresAt: expiresAt, Attempts: attempts}, pin.String()
}

// liveChallengeExpiry is well after the fake clock (time.Unix(1000, 0)), so a
// challenge with this expiry is live.
var liveChallengeExpiry = time.Unix(1_000_000, 0).UTC()

func TestVerify_SuccessActivatesAndClears(t *testing.T) {
	challenge, pin := pinChallenge(t, 0, liveChallengeExpiry)
	users := &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}}
	verify := &fakeRegistrationVerifications{findResults: []verificationResult{{verification: challenge}}}
	svc, tx := newRegistrationServiceForTest(users, verify, &recordingVerificationSender{})

	if err := svc.Verify(context.Background(), VerifyParams{MailAddress: aliceMail(t), PIN: pin}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if users.lastSetStatus != domain.UserStatusActive || users.setStatusCalls != 1 {
		t.Fatalf("SetStatus: calls=%d last=%q, want 1 call to active", users.setStatusCalls, users.lastSetStatus)
	}
	if verify.deleteCalls != 1 {
		t.Fatalf("challenge deletes = %d, want 1", verify.deleteCalls)
	}
	if verify.incrementCalls != 0 {
		t.Fatalf("attempt increments = %d, want 0 on success", verify.incrementCalls)
	}
	if tx.commits != 1 {
		t.Fatalf("commits = %d, want 1", tx.commits)
	}
}

func TestVerify_WrongPINCountsAttemptAndFails(t *testing.T) {
	challenge, _ := pinChallenge(t, 0, liveChallengeExpiry)
	users := &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}}
	verify := &fakeRegistrationVerifications{findResults: []verificationResult{{verification: challenge}}}
	svc, tx := newRegistrationServiceForTest(users, verify, &recordingVerificationSender{})

	err := svc.Verify(context.Background(), VerifyParams{MailAddress: aliceMail(t), PIN: "000000"})
	if !errors.Is(err, errVerifyInvalid) {
		t.Fatalf("Verify err = %v, want errVerifyInvalid", err)
	}
	if verify.incrementCalls != 1 {
		t.Fatalf("attempt increments = %d, want 1", verify.incrementCalls)
	}
	if users.setStatusCalls != 0 {
		t.Fatalf("SetStatus calls = %d, want 0 on a wrong PIN", users.setStatusCalls)
	}
	if tx.commits != 1 {
		t.Fatalf("commits = %d, want 1 so the attempt count persists", tx.commits)
	}
}

func TestVerify_LockedRejectsWithoutCounting(t *testing.T) {
	challenge, pin := pinChallenge(t, 5, liveChallengeExpiry) // attempts == MaxVerifyAttempts
	users := &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}}
	verify := &fakeRegistrationVerifications{findResults: []verificationResult{{verification: challenge}}}
	svc, _ := newRegistrationServiceForTest(users, verify, &recordingVerificationSender{})

	// Even with the correct PIN, a locked challenge is rejected.
	err := svc.Verify(context.Background(), VerifyParams{MailAddress: aliceMail(t), PIN: pin})
	if !errors.Is(err, errVerifyInvalid) {
		t.Fatalf("Verify err = %v, want errVerifyInvalid", err)
	}
	if verify.incrementCalls != 0 || users.setStatusCalls != 0 {
		t.Fatalf("locked challenge must not increment (%d) or activate (%d)", verify.incrementCalls, users.setStatusCalls)
	}
}

func TestVerify_NonPendingAccountIsUniformAndDoesNotReadChallenge(t *testing.T) {
	for _, status := range []domain.UserStatus{domain.UserStatusActive, domain.UserStatusDisabled} {
		t.Run(string(status), func(t *testing.T) {
			users := &fakeRegistrationUsers{
				findResults: []userResult{{user: registrationUser(t, 42, "A000000042", status)}},
			}
			verify := &fakeRegistrationVerifications{}
			svc, _ := newRegistrationServiceForTest(users, verify, &recordingVerificationSender{})

			err := svc.Verify(context.Background(), VerifyParams{MailAddress: aliceMail(t), PIN: "123456"})
			if !errors.Is(err, errVerifyInvalid) {
				t.Fatalf("Verify err = %v, want errVerifyInvalid", err)
			}
			if verify.findCalls != 0 || verify.incrementCalls != 0 || users.setStatusCalls != 0 {
				t.Fatalf("non-pending account must not read challenge (%d), increment (%d), or activate (%d)",
					verify.findCalls, verify.incrementCalls, users.setStatusCalls)
			}
		})
	}
}

func TestVerify_ExpiredFails(t *testing.T) {
	challenge, pin := pinChallenge(t, 0, time.Unix(500, 0).UTC()) // before the fake clock
	users := &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}}
	verify := &fakeRegistrationVerifications{findResults: []verificationResult{{verification: challenge}}}
	svc, _ := newRegistrationServiceForTest(users, verify, &recordingVerificationSender{})

	if err := svc.Verify(context.Background(), VerifyParams{MailAddress: aliceMail(t), PIN: pin}); !errors.Is(err, errVerifyInvalid) {
		t.Fatalf("Verify err = %v, want errVerifyInvalid for an expired challenge", err)
	}
	if verify.incrementCalls != 0 || users.setStatusCalls != 0 {
		t.Fatal("an expired challenge must not increment or activate")
	}
}

func TestVerify_UnknownAccountOrChallengeIsUniform(t *testing.T) {
	tests := []struct {
		name   string
		users  *fakeRegistrationUsers
		verify *fakeRegistrationVerifications
	}{
		{
			name:   "no account",
			users:  &fakeRegistrationUsers{findResults: []userResult{{err: persistence.ErrUserNotFound}}},
			verify: &fakeRegistrationVerifications{},
		},
		{
			name:   "no challenge",
			users:  &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}},
			verify: &fakeRegistrationVerifications{findResults: []verificationResult{{err: verifyrepo.ErrVerificationNotFound}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newRegistrationServiceForTest(tc.users, tc.verify, &recordingVerificationSender{})
			if err := svc.Verify(context.Background(), VerifyParams{MailAddress: aliceMail(t), PIN: "123456"}); !errors.Is(err, errVerifyInvalid) {
				t.Fatalf("Verify err = %v, want errVerifyInvalid", err)
			}
		})
	}
}

func TestResend_PendingSendsFreshPIN(t *testing.T) {
	sender := &recordingVerificationSender{}
	users := &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}}
	verify := &fakeRegistrationVerifications{resendResults: []error{nil}}
	svc, tx := newRegistrationServiceForTest(users, verify, sender)

	if err := svc.Resend(context.Background(), ResendParams{MailAddress: aliceMail(t)}); err != nil {
		t.Fatalf("Resend: %v", err)
	}
	if verify.resendCalls != 1 || sender.calls != 1 {
		t.Fatalf("resends=%d sends=%d, want 1 and 1", verify.resendCalls, sender.calls)
	}
	if tx.commits != 1 {
		t.Fatalf("commits = %d, want 1", tx.commits)
	}
}

func TestResend_NonPendingOrUnknownIsSilentNoOp(t *testing.T) {
	tests := []struct {
		name  string
		users *fakeRegistrationUsers
	}{
		{"active account", &fakeRegistrationUsers{findResults: []userResult{{user: registrationUser(t, 42, "A000000042", domain.UserStatusActive)}}}},
		{"unknown account", &fakeRegistrationUsers{findResults: []userResult{{err: persistence.ErrUserNotFound}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sender := &recordingVerificationSender{}
			verify := &fakeRegistrationVerifications{}
			svc, _ := newRegistrationServiceForTest(tc.users, verify, sender)
			if err := svc.Resend(context.Background(), ResendParams{MailAddress: aliceMail(t)}); err != nil {
				t.Fatalf("Resend = %v, want nil (uniform no-op)", err)
			}
			if verify.resendCalls != 0 || sender.calls != 0 {
				t.Fatalf("a no-op must not resend (%d) or send (%d)", verify.resendCalls, sender.calls)
			}
		})
	}
}

func TestResend_RateLimitedSurfacesAndDoesNotSend(t *testing.T) {
	sender := &recordingVerificationSender{}
	users := &fakeRegistrationUsers{findResults: []userResult{{user: pendingAlice(t)}}}
	verify := &fakeRegistrationVerifications{resendResults: []error{verifyrepo.ErrVerificationRateLimited}}
	svc, _ := newRegistrationServiceForTest(users, verify, sender)

	if err := svc.Resend(context.Background(), ResendParams{MailAddress: aliceMail(t)}); !errors.Is(err, verifyrepo.ErrVerificationRateLimited) {
		t.Fatalf("Resend err = %v, want ErrVerificationRateLimited", err)
	}
	if sender.calls != 0 {
		t.Fatalf("a rate-limited resend must not send (%d)", sender.calls)
	}
}
