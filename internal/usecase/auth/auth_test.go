package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/common/security"
	"vending/internal/model"
	machinemock "vending/internal/repository/machine/mock"
	sessionmock "vending/internal/repository/session/mock"
	"vending/internal/repository/uow/uowtest"
	usermock "vending/internal/repository/user/mock"
	"vending/internal/usecase/auth"
)

// The rules in here are the ones that are hardest to be sure of by reading, and
// the ones it matters most to be sure of: whether a wrong password is counted,
// whether a locked account stays locked, whether an idle token is refused, and
// whether one machine's operator token works on another. With every boundary
// mocked, each is a test rather than a hope.

var (
	t0           = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	adminIdle    = 30 * time.Minute
	operatorIdle = 2 * time.Minute
	vm           = model.Machine{ID: 9, Code: "VM-01", Name: "Depan"}
)

// password is a real argon2 hash, so the verify step is the real one: a test
// that stubbed out password checking would not be testing the login rule.
var password = func() string {
	h, err := security.HashPassword("rahasia123")
	if err != nil {
		panic(err)
	}
	return h
}()

type fixture struct {
	unit     *uowtest.Bundle
	users    *usermock.MockRepository
	sessions *sessionmock.MockRepository
	machines *machinemock.MockRepository
	uc       *auth.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		users:    usermock.NewMockRepository(ctrl),
		sessions: sessionmock.NewMockRepository(ctrl),
		machines: machinemock.NewMockRepository(ctrl),
	}
	f.unit = &uowtest.Bundle{UserRepo: f.users, MachineRepo: f.machines}
	f.uc = auth.New(f.unit, f.sessions, f.machines,
		clock.New(func() time.Time { return t0 }), adminIdle, operatorIdle, nil)
	return f, ctrl
}

func admin() model.User {
	return model.User{ID: 3, Name: "Admin", Username: "admin", PasswordHash: password,
		Role: model.RoleAdmin, IsActive: true}
}

func operator() model.User {
	return model.User{ID: 4, Name: "Budi", Username: "budi", PasswordHash: password,
		Role: model.RoleOperator, IsActive: true}
}

func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Code
}

func extra(t *testing.T, err error) map[string]any {
	t.Helper()
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Extra
}

// ---------------------------------------------------------------- signing in

func TestAdminLoginIssuesASessionForTheRightPassword(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(admin(), nil)
	f.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, s model.Session) error {
			if s.UserID != 3 || s.Kind != model.KindAdmin {
				t.Errorf("session = %+v", s)
			}
			// An admin session belongs to no machine.
			if s.MachineID != nil {
				t.Errorf("machineID = %v, want nil", *s.MachineID)
			}
			// A new session counts as just used, or it starts out idle.
			if !s.LastSeenAt.Equal(t0) || !s.CreatedAt.Equal(t0) {
				t.Errorf("session times = %+v", s)
			}
			// What is stored is the hash, never the token.
			if s.TokenHash == "" {
				t.Error("no token hash stored")
			}
			return nil
		})

	got, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Admin" || got.Username != "admin" {
		t.Errorf("login = %+v", got)
	}
	if len(got.Token) < 10 {
		t.Errorf("token = %q, want a real one", got.Token)
	}
}

// The username is matched in lower case and trimmed, so "  Admin " is the same
// person. That is a rule about accounts, not something the table does.
func TestLoginNormalizesTheUsername(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(admin(), nil)
	f.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	if _, err := f.uc.AdminLogin(context.Background(), "  ADMIN  ", "rahasia123"); err != nil {
		t.Fatal(err)
	}
}

// Restocking a machine is not permission to manage the system (TC-21). The
// password was right, so the refusal says what is actually wrong.
func TestAdminLoginRefusesAnOperatorAccount(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockForUpdate(gomock.Any(), "budi").Return(operator(), nil)
	// No session is created.

	_, err := f.uc.AdminLogin(context.Background(), "budi", "rahasia123")
	if got := code(t, err); got != model.CodeForbidden {
		t.Errorf("code = %s, want %s", got, model.CodeForbidden)
	}
}

// An admin restocking a machine is expected, so the operator endpoint takes
// either role.
func TestOperatorLoginAcceptsBothRolesAndBindsTheSessionToTheMachine(t *testing.T) {
	for _, u := range []model.User{operator(), admin()} {
		t.Run(u.Role, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.users.EXPECT().LockForUpdate(gomock.Any(), u.Username).Return(u, nil)
			f.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, s model.Session) error {
					if s.Kind != model.KindOperator {
						t.Errorf("kind = %s, want %s", s.Kind, model.KindOperator)
					}
					if s.MachineID == nil || *s.MachineID != vm.ID {
						t.Errorf("machineID = %v, want %d", s.MachineID, vm.ID)
					}
					return nil
				})

			if _, err := f.uc.OperatorLogin(context.Background(), vm, u.Username, "rahasia123"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// An unknown username and a wrong password give the same code and the same
// message, so neither can be used to find out which accounts exist (FR-13).
func TestAnUnknownUsernameLooksExactlyLikeAWrongPassword(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockForUpdate(gomock.Any(), "hantu").Return(model.User{}, model.ErrNoRecord)

	_, unknown := f.uc.AdminLogin(context.Background(), "hantu", "apa-saja")
	if got := code(t, unknown); got != model.CodeInvalidLogin {
		t.Fatalf("code = %s, want %s", got, model.CodeInvalidLogin)
	}
	// And it says nothing about tries left, because there is no account to
	// count tries against: that alone would give the answer away.
	if extra(t, unknown) != nil {
		t.Errorf("extra = %v, want nothing", extra(t, unknown))
	}

	g, ctrl2 := newFixture(t)
	defer ctrl2.Finish()
	g.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(admin(), nil)
	g.users.EXPECT().SetFailedLogins(gomock.Any(), int64(3), 1).Return(nil)
	_, wrong := g.uc.AdminLogin(context.Background(), "admin", "salah")

	ue, _ := model.AsError(unknown)
	we, _ := model.AsError(wrong)
	if ue.Code != we.Code || ue.Message != we.Message {
		t.Errorf("unknown username %q/%q differs from wrong password %q/%q",
			ue.Code, ue.Message, we.Code, we.Message)
	}
}

// Each wrong password is counted, and the fifth shuts the account for five
// minutes (FR-13, TC-19).
func TestTheFifthWrongPasswordLocksTheAccount(t *testing.T) {
	for _, tc := range []struct {
		already int
		want    string
		left    any
	}{
		{0, model.CodeInvalidLogin, 4},
		{1, model.CodeInvalidLogin, 3},
		{3, model.CodeInvalidLogin, 1},
		{4, model.CodeLocked, nil},
	} {
		t.Run(string(rune('0'+tc.already))+" already wrong", func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			u := admin()
			u.FailedLogins = tc.already
			f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(u, nil)
			if tc.want == model.CodeLocked {
				f.users.EXPECT().LockUntil(gomock.Any(), u.ID, t0.Add(5*time.Minute)).Return(nil)
			} else {
				f.users.EXPECT().SetFailedLogins(gomock.Any(), u.ID, tc.already+1).Return(nil)
			}

			_, err := f.uc.AdminLogin(context.Background(), "admin", "salah")
			if got := code(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s", got, tc.want)
			}
			if tc.left != nil {
				if got := extra(t, err)["attemptsLeft"]; got != tc.left {
					t.Errorf("attemptsLeft = %v, want %v", got, tc.left)
				}
			}
		})
	}
}

// A refusal has to be remembered, or the counter never reaches five and the
// lockout can be walked straight past. So the unit of work must commit even
// though the request failed: the count is written, and the refusal is carried
// out separately rather than returned as an error from inside it.
func TestAWrongPasswordIsRememberedEvenThoughTheRequestFails(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	u := admin()
	u.FailedLogins = 2
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(u, nil)
	written := false
	f.users.EXPECT().SetFailedLogins(gomock.Any(), u.ID, 3).
		DoAndReturn(func(context.Context, int64, int) error { written = true; return nil })

	if _, err := f.uc.AdminLogin(context.Background(), "admin", "salah"); err == nil {
		t.Fatal("want a refusal")
	}
	if !written {
		t.Error("the wrong password was not counted, so the lockout can never trigger")
	}
}

// While an account is locked even the right password is refused, or the lock
// could be used to test passwords (TC-19). The password is not even checked.
func TestALockedAccountRefusesTheRightPassword(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	until := t0.Add(3 * time.Minute)
	u := admin()
	u.LockedUntil = &until
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(u, nil)
	// Nothing is written and no session is made.

	_, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123")
	if got := code(t, err); got != model.CodeLocked {
		t.Fatalf("code = %s, want %s", got, model.CodeLocked)
	}
	// The screen needs the exact seconds for its countdown; the message rounds
	// up to whole minutes, because that is what a person can act on.
	if got := extra(t, err)["retryInSeconds"]; got != 180 {
		t.Errorf("retryInSeconds = %v, want 180", got)
	}
}

// Once the lock has run out the account works again, and the counter starts
// over rather than resuming at five.
func TestAnExpiredLockLetsTheAccountBackIn(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	past := t0.Add(-time.Second)
	u := admin()
	u.LockedUntil = &past
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(u, nil)
	f.users.EXPECT().ClearFailedLogins(gomock.Any(), u.ID).Return(nil)
	f.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	if _, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123"); err != nil {
		t.Fatal(err)
	}
}

// A right password after some wrong ones clears the count, so yesterday's typos
// do not add up to a lockout today.
func TestARightPasswordForgetsTheWrongOnes(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	u := admin()
	u.FailedLogins = 3
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(u, nil)
	f.users.EXPECT().ClearFailedLogins(gomock.Any(), u.ID).Return(nil)
	f.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	if _, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123"); err != nil {
		t.Fatal(err)
	}
}

// A clean account is not written to on a successful login: there is nothing to
// clear, and a write per sign-in is a write for nothing.
func TestACleanAccountIsNotWrittenToOnLogin(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(admin(), nil)
	f.sessions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	// No ClearFailedLogins is expected.

	if _, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123"); err != nil {
		t.Fatal(err)
	}
}

// A deactivated account is told so rather than being left to look like a typo:
// the password was right, so the person is who they say they are.
func TestADeactivatedAccountIsRefusedWithAReason(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	u := admin()
	u.IsActive = false
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(u, nil)

	_, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123")
	if got := code(t, err); got != model.CodeForbidden {
		t.Errorf("code = %s, want %s", got, model.CodeForbidden)
	}
}

// ---------------------------------------------------------------- signing out

func TestLogoutEndsTheSessionByItsTokenHash(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.sessions.EXPECT().DeleteByTokenHash(gomock.Any(), security.HashToken("adm_abc")).Return(nil)

	if err := f.uc.Logout(context.Background(), "adm_abc"); err != nil {
		t.Fatal(err)
	}
}

// Signing out without a token is not a failure, and must not reach the
// database: an empty token hashes to something, and that something could match
// a row.
func TestLogoutWithoutATokenDoesNothing(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// No repository call is expected.

	if err := f.uc.Logout(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- staying in

func live(kind string, lastSeen time.Time, machineID int64, role string) model.AuthSession {
	return model.AuthSession{
		Principal: model.Principal{
			SessionID: 77, UserID: 3, Name: "Admin", Username: "admin",
			Role: role, MachineID: machineID, StartedAt: lastSeen,
		},
		LastSeenAt: lastSeen,
		UserActive: true,
	}
}

// The token is looked up by its hash and by its kind together, so an operator
// token can never be presented as an admin one.
func TestAdminSessionLooksUpTheHashAndTheKind(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.sessions.EXPECT().
		ByTokenHash(gomock.Any(), model.KindAdmin, security.HashToken("adm_abc")).
		Return(live(model.KindAdmin, t0.Add(-time.Minute), 0, model.RoleAdmin), nil)
	f.sessions.EXPECT().TouchSeen(gomock.Any(), int64(77), t0).Return(nil)

	p, err := f.uc.AdminSession(context.Background(), "adm_abc")
	if err != nil {
		t.Fatal(err)
	}
	if p.SessionID != 77 || p.Username != "admin" {
		t.Errorf("principal = %+v", p)
	}
}

// The idle timeouts are BR-13: 30 minutes for the admin panel, 2 minutes for a
// kiosk, because a kiosk is a screen standing in public.
func TestASessionIsRefusedOnceItHasGoneIdle(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		idle  time.Duration
		valid bool
	}{
		{"admin just inside 30 minutes", model.KindAdmin, adminIdle - time.Second, true},
		{"admin just past 30 minutes", model.KindAdmin, adminIdle + time.Second, false},
		{"operator just inside 2 minutes", model.KindOperator, operatorIdle - time.Second, true},
		{"operator just past 2 minutes", model.KindOperator, operatorIdle + time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			machineID := int64(0)
			if tc.kind == model.KindOperator {
				machineID = vm.ID
			}
			f.sessions.EXPECT().ByTokenHash(gomock.Any(), tc.kind, gomock.Any()).
				Return(live(tc.kind, t0.Add(-tc.idle), machineID, model.RoleAdmin), nil)
			if !tc.valid {
				// An idle token is worthless, so the row goes rather than being
				// re-judged on every later request.
				f.sessions.EXPECT().Delete(gomock.Any(), int64(77)).Return(nil)
			} else {
				f.sessions.EXPECT().TouchSeen(gomock.Any(), int64(77), t0).Return(nil)
			}

			var err error
			if tc.kind == model.KindAdmin {
				_, err = f.uc.AdminSession(context.Background(), "adm_abc")
			} else {
				_, err = f.uc.OperatorSession(context.Background(), vm, "opr_abc")
			}
			if tc.valid {
				if err != nil {
					t.Fatalf("err = %v, want the session to hold", err)
				}
				return
			}
			if got := code(t, err); got != model.CodeUnauthorized {
				t.Errorf("code = %s, want %s", got, model.CodeUnauthorized)
			}
		})
	}
}

// An operator token is good on the machine it was issued for and nowhere else,
// or one kiosk's token could be used to restock another.
func TestAnOperatorTokenIsRefusedOnAnotherMachine(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.sessions.EXPECT().ByTokenHash(gomock.Any(), model.KindOperator, gomock.Any()).
		Return(live(model.KindOperator, t0, 404, model.RoleOperator), nil)
	f.sessions.EXPECT().Delete(gomock.Any(), int64(77)).Return(nil)

	_, err := f.uc.OperatorSession(context.Background(), vm, "opr_abc")
	if got := code(t, err); got != model.CodeUnauthorized {
		t.Errorf("code = %s, want %s", got, model.CodeUnauthorized)
	}
}

// An account demoted or deactivated while signed in loses its session at the
// next request, rather than keeping admin rights until it goes idle.
func TestASessionEndsWhenTheAccountChangesUnderIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   model.AuthSession
	}{
		{"deactivated", func() model.AuthSession {
			a := live(model.KindAdmin, t0, 0, model.RoleAdmin)
			a.UserActive = false
			return a
		}()},
		{"demoted to operator", live(model.KindAdmin, t0, 0, model.RoleOperator)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.sessions.EXPECT().ByTokenHash(gomock.Any(), model.KindAdmin, gomock.Any()).Return(tc.at, nil)
			f.sessions.EXPECT().Delete(gomock.Any(), int64(77)).Return(nil)

			_, err := f.uc.AdminSession(context.Background(), "adm_abc")
			if got := code(t, err); got != model.CodeUnauthorized {
				t.Errorf("code = %s, want %s", got, model.CodeUnauthorized)
			}
		})
	}
}

// An empty token is refused without a lookup: hashing "" gives a real hash, and
// a real hash can match a row.
func TestAnEmptyTokenIsRefusedWithoutALookup(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// No repository call is expected.

	if got := code(t, mustFail(f.uc.AdminSession(context.Background(), ""))); got != model.CodeUnauthorized {
		t.Errorf("code = %s, want %s", got, model.CodeUnauthorized)
	}
}

// The last-used time is written at most every 15 seconds. The timeouts are
// minutes, so a write on every single request would buy nothing.
func TestTheLastUsedTimeIsNotWrittenOnEveryRequest(t *testing.T) {
	cases := []struct {
		since time.Duration
		write bool
	}{
		{time.Second, false},
		{14 * time.Second, false},
		{15 * time.Second, true},
		{time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.since.String(), func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.sessions.EXPECT().ByTokenHash(gomock.Any(), model.KindAdmin, gomock.Any()).
				Return(live(model.KindAdmin, t0.Add(-tc.since), 0, model.RoleAdmin), nil)
			if tc.write {
				f.sessions.EXPECT().TouchSeen(gomock.Any(), int64(77), t0).Return(nil)
			}

			if _, err := f.uc.AdminSession(context.Background(), "adm_abc"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// ---------------------------------------------------------------- the agent

func TestMachineResolvesAKeyAndRecordsTheContact(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().ByAPIKeyHash(gomock.Any(), security.HashToken("vmk_abc")).
		Return(model.MachineRecord{ID: 9, Code: "VM-01", Name: "Depan", IsActive: true}, nil)
	f.machines.EXPECT().TouchSeen(gomock.Any(), int64(9), t0).Return(nil)

	got, err := f.uc.Machine(context.Background(), "vmk_abc")
	if err != nil {
		t.Fatal(err)
	}
	if got != vm {
		t.Errorf("machine = %+v, want %+v", got, vm)
	}
}

// The contact time is written at most every 30 seconds. An agent polls
// constantly, and "kontak terakhir" is shown to the minute.
func TestTheContactTimeIsNotWrittenOnEveryCall(t *testing.T) {
	cases := []struct {
		name  string
		seen  *time.Time
		write bool
	}{
		{"never seen", nil, true},
		{"seen a second ago", ptr(t0.Add(-time.Second)), false},
		{"seen 29 seconds ago", ptr(t0.Add(-29 * time.Second)), false},
		{"seen 30 seconds ago", ptr(t0.Add(-30 * time.Second)), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.machines.EXPECT().ByAPIKeyHash(gomock.Any(), gomock.Any()).
				Return(model.MachineRecord{ID: 9, Code: "VM-01", Name: "Depan", IsActive: true, LastSeenAt: tc.seen}, nil)
			if tc.write {
				f.machines.EXPECT().TouchSeen(gomock.Any(), int64(9), t0).Return(nil)
			}

			if _, err := f.uc.Machine(context.Background(), "vmk_abc"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMachineRefusesAnUnknownOrMissingKey(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().ByAPIKeyHash(gomock.Any(), gomock.Any()).
			Return(model.MachineRecord{}, model.ErrNoRecord)

		_, err := f.uc.Machine(context.Background(), "vmk_salah")
		if got := code(t, err); got != model.CodeInvalidMachineKey {
			t.Errorf("code = %s, want %s", got, model.CodeInvalidMachineKey)
		}
	})
	t.Run("missing", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		// No lookup is made for an empty key.

		_, err := f.uc.Machine(context.Background(), "")
		if got := code(t, err); got != model.CodeInvalidMachineKey {
			t.Errorf("code = %s, want %s", got, model.CodeInvalidMachineKey)
		}
	})
}

// A deactivated machine is told apart from an unknown key: the agent is
// genuine, so whoever is looking after it deserves to know why it stopped.
func TestADeactivatedMachineIsToldApartFromAnUnknownKey(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().ByAPIKeyHash(gomock.Any(), gomock.Any()).
		Return(model.MachineRecord{ID: 9, Code: "VM-01", IsActive: false}, nil)
	// No contact is recorded: the machine is not allowed to be talking to us.

	_, err := f.uc.Machine(context.Background(), "vmk_abc")
	if got := code(t, err); got != model.CodeMachineInactive {
		t.Errorf("code = %s, want %s", got, model.CodeMachineInactive)
	}
}

// A storage failure is reported as itself, so an outage is not shown to someone
// signing in as "wrong password".
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	boom := errors.New("the users table is unreachable")
	f.users.EXPECT().LockForUpdate(gomock.Any(), "admin").Return(model.User{}, boom)

	if _, err := f.uc.AdminLogin(context.Background(), "admin", "rahasia123"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}

func ptr(t time.Time) *time.Time { return &t }

// mustFail returns the error of a call whose value is not wanted.
func mustFail(_ model.Principal, err error) error { return err }
