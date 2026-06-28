package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oklahomer/blabby/internal/domain"
	"github.com/oklahomer/blabby/internal/id"
	"github.com/oklahomer/blabby/internal/persistence/postgres"
	"github.com/oklahomer/blabby/internal/persistence/userrepo"
	"github.com/oklahomer/blabby/internal/persistence/verifyrepo"
	"github.com/oklahomer/blabby/internal/verification"
)

type userResult struct {
	user userrepo.User
	err  error
}

type fakeRegistrationUsers struct {
	findResults   []userResult
	createResults []userResult
	lastCreate    userrepo.CreateParams
	findCalls     int
	createCalls   int
}

func (f *fakeRegistrationUsers) FindByEmail(context.Context, postgres.Querier, domain.MailAddress) (userrepo.User, error) {
	if f.findCalls >= len(f.findResults) {
		return userrepo.User{}, errors.New("unexpected FindByEmail")
	}
	result := f.findResults[f.findCalls]
	f.findCalls++
	return result.user, result.err
}

func (f *fakeRegistrationUsers) Create(_ context.Context, _ postgres.Querier, params userrepo.CreateParams) (userrepo.User, error) {
	if f.createCalls >= len(f.createResults) {
		return userrepo.User{}, errors.New("unexpected Create")
	}
	f.lastCreate = params
	result := f.createResults[f.createCalls]
	f.createCalls++
	return result.user, result.err
}

type fakeRegistrationVerifications struct {
	createResults []error
	resendResults []error
	lastCreate    verifyrepo.CreateParams
	lastResend    verifyrepo.ResendParams
	lastPolicy    verifyrepo.ResendPolicy
	createCalls   int
	resendCalls   int
}

func (f *fakeRegistrationVerifications) Create(_ context.Context, _ postgres.Querier, params verifyrepo.CreateParams) error {
	if f.createCalls >= len(f.createResults) {
		return errors.New("unexpected verification Create")
	}
	f.lastCreate = params
	err := f.createResults[f.createCalls]
	f.createCalls++
	return err
}

func (f *fakeRegistrationVerifications) Resend(_ context.Context, _ postgres.Querier, params verifyrepo.ResendParams, policy verifyrepo.ResendPolicy) error {
	if f.resendCalls >= len(f.resendResults) {
		return errors.New("unexpected verification Resend")
	}
	f.lastResend = params
	f.lastPolicy = policy
	err := f.resendResults[f.resendCalls]
	f.resendCalls++
	return err
}

type fakeRegistrationTx struct {
	calls   int
	commits int
}

func (tx *fakeRegistrationTx) WithinTx(ctx context.Context, fn func(q postgres.Querier) error) error {
	tx.calls++
	err := fn(nil)
	if err == nil {
		tx.commits++
	}
	return err
}

type recordingVerificationSender struct {
	err   error
	calls int
	to    domain.MailAddress
	pin   verification.PIN
}

func (s *recordingVerificationSender) Send(_ context.Context, to domain.MailAddress, pin verification.PIN, _ time.Duration) error {
	s.calls++
	s.to = to
	s.pin = pin
	return s.err
}

func registrationParams(t *testing.T) RegisterParams {
	t.Helper()
	mail, err := domain.NewMailAddress("alice@example.com")
	if err != nil {
		t.Fatalf("NewMailAddress: %v", err)
	}
	handle, err := domain.NewHandle("Alice")
	if err != nil {
		t.Fatalf("NewHandle: %v", err)
	}
	return RegisterParams{
		MailAddress: mail,
		Handle:      handle,
		Password:    "supersecret12",
	}
}

func pendingAlice(t *testing.T) userrepo.User {
	t.Helper()
	return registrationUser(t, 42, "A000000042", domain.UserStatusPending)
}

func registrationUser(t *testing.T, rawID int64, rawCode string, status domain.UserStatus) userrepo.User {
	t.Helper()
	userID, err := id.NewUserID(rawID)
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	code, err := id.ParsePublicCode(rawCode)
	if err != nil {
		t.Fatalf("ParsePublicCode: %v", err)
	}
	mail, err := domain.NewMailAddress("alice@example.com")
	if err != nil {
		t.Fatalf("NewMailAddress: %v", err)
	}
	handle, err := domain.NewHandle("Alice")
	if err != nil {
		t.Fatalf("NewHandle: %v", err)
	}
	now := time.Unix(0, 0).UTC()
	return userrepo.User{
		ID:           userID,
		PublicCode:   code,
		MailAddress:  mail,
		Handle:       handle,
		DisplayName:  handle.Display(),
		PasswordHash: []byte("$2a$12$hash"),
		Status:       status,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func newRegistrationServiceForTest(users registrationUsers, verify registrationVerifications, sender verification.Sender) (*RegistrationService, *fakeRegistrationTx) {
	tx := &fakeRegistrationTx{}
	svc := &RegistrationService{
		users:  users,
		verify: verify,
		sender: sender,
		tx:     tx,
		now:    func() time.Time { return time.Unix(1000, 0).UTC() },
		policy: RegistrationPolicy{
			PinTTL:            time.Minute,
			ResendMinInterval: time.Minute,
			MaxResendCount:    5,
			CollisionRetries:  3,
		},
	}
	return svc, tx
}

func TestRegistrationService_NewAccountSendsPIN(t *testing.T) {
	sender := &recordingVerificationSender{}
	users := &fakeRegistrationUsers{
		findResults: []userResult{
			{err: userrepo.ErrUserNotFound},
		},
		createResults: []userResult{
			{user: registrationUser(t, 99, "A000000099", domain.UserStatusPending)},
		},
	}
	verify := &fakeRegistrationVerifications{
		createResults: []error{nil},
	}
	svc, tx := newRegistrationServiceForTest(users, verify, sender)

	result, err := svc.Register(context.Background(), registrationParams(t))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.PublicCode != "UA000000099" {
		t.Fatalf("PublicCode = %q, want UA000000099", result.PublicCode)
	}
	if tx.commits != 1 {
		t.Fatalf("commits = %d, want 1", tx.commits)
	}
	if users.createCalls != 1 {
		t.Fatalf("user creates = %d, want 1", users.createCalls)
	}
	if verify.createCalls != 1 {
		t.Fatalf("verification creates = %d, want 1", verify.createCalls)
	}
	if sender.calls != 1 {
		t.Fatalf("sender calls = %d, want 1", sender.calls)
	}
	if users.lastCreate.DisplayName != "Alice" {
		t.Fatalf("display name = %q, want Alice", users.lastCreate.DisplayName)
	}
}

func TestRegistrationService_DeliveryFailureIsBestEffort(t *testing.T) {
	sender := &recordingVerificationSender{err: errors.New("smtp down")}
	users := &fakeRegistrationUsers{
		findResults: []userResult{
			{user: pendingAlice(t)},
		},
	}
	verify := &fakeRegistrationVerifications{
		resendResults: []error{nil},
	}
	svc, tx := newRegistrationServiceForTest(users, verify, sender)

	result, err := svc.Register(context.Background(), registrationParams(t))
	if err != nil {
		t.Fatalf("Register should not surface a best-effort delivery failure: %v", err)
	}
	if result.PublicCode != "UA000000042" {
		t.Fatalf("PublicCode = %q, want pending account code", result.PublicCode)
	}
	if tx.commits != 1 {
		t.Fatalf("commits = %d, want 1", tx.commits)
	}
	if verify.resendCalls != 1 {
		t.Fatalf("verification resends = %d, want 1", verify.resendCalls)
	}
	if sender.calls != 1 {
		t.Fatalf("sender calls = %d, want 1", sender.calls)
	}
}

func TestRegistrationService_EmailInsertRaceRetriesAsPendingResend(t *testing.T) {
	sender := &recordingVerificationSender{}
	users := &fakeRegistrationUsers{
		findResults: []userResult{
			{err: userrepo.ErrUserNotFound},
			{user: pendingAlice(t)},
		},
		createResults: []userResult{
			{err: userrepo.ErrMailAddressTaken},
		},
	}
	verify := &fakeRegistrationVerifications{
		resendResults: []error{nil},
	}
	svc, tx := newRegistrationServiceForTest(users, verify, sender)

	result, err := svc.Register(context.Background(), registrationParams(t))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.PublicCode != "UA000000042" {
		t.Fatalf("PublicCode = %q, want pending account code", result.PublicCode)
	}
	if tx.calls != 2 {
		t.Fatalf("transactions = %d, want 2 (initial insert race + retry)", tx.calls)
	}
	if tx.commits != 1 {
		t.Fatalf("commits = %d, want 1", tx.commits)
	}
	if verify.resendCalls != 1 {
		t.Fatalf("verification resends = %d, want 1", verify.resendCalls)
	}
	if sender.calls != 1 {
		t.Fatalf("sender calls = %d, want 1", sender.calls)
	}
}

func TestRegistrationService_PendingResendRateLimitedDoesNotSend(t *testing.T) {
	sender := &recordingVerificationSender{}
	users := &fakeRegistrationUsers{
		findResults: []userResult{
			{user: pendingAlice(t)},
		},
	}
	verify := &fakeRegistrationVerifications{
		resendResults: []error{verifyrepo.ErrVerificationRateLimited},
	}
	svc, tx := newRegistrationServiceForTest(users, verify, sender)

	_, err := svc.Register(context.Background(), registrationParams(t))
	if !errors.Is(err, verifyrepo.ErrVerificationRateLimited) {
		t.Fatalf("Register err = %v, want ErrVerificationRateLimited", err)
	}
	if tx.commits != 0 {
		t.Fatalf("commits = %d, want 0", tx.commits)
	}
	if sender.calls != 0 {
		t.Fatalf("sender calls = %d, want 0", sender.calls)
	}
}
