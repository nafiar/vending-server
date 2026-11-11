package operator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/common/security"
	"vending/internal/model"
	auditmock "vending/internal/repository/audit/mock"
	reportmock "vending/internal/repository/report/mock"
	sessionmock "vending/internal/repository/session/mock"
	"vending/internal/repository/uow/uowtest"
	usermock "vending/internal/repository/user/mock"
	"vending/internal/usecase/operator"
)

// The rule this package exists to defend is that an admin manages operators and
// nothing else: an admin who could edit admins could quietly promote
// themselves, and the change log would record it as an ordinary edit. Most of
// what follows is that boundary, from both sides.

var (
	t0      = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	jakarta = func() *time.Location {
		loc, err := time.LoadLocation("Asia/Jakarta")
		if err != nil {
			panic(err)
		}
		return loc
	}()
	actor = model.Principal{UserID: 3, Name: "Admin", Username: "admin", Role: model.RoleAdmin}
)

type fixture struct {
	unit     *uowtest.Bundle
	users    *usermock.MockRepository
	sessions *sessionmock.MockRepository
	audit    *auditmock.MockRepository
	reports  *reportmock.MockRepository
	uc       *operator.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		users:    usermock.NewMockRepository(ctrl),
		sessions: sessionmock.NewMockRepository(ctrl),
		audit:    auditmock.NewMockRepository(ctrl),
		reports:  reportmock.NewMockRepository(ctrl),
	}
	f.unit = &uowtest.Bundle{UserRepo: f.users, SessionRepo: f.sessions, AuditRepo: f.audit}
	f.uc = operator.New(f.unit, f.users, f.reports,
		clock.New(func() time.Time { return t0 }), jakarta, nil)
	return f, ctrl
}

func budi() model.User {
	return model.User{
		ID: 4, Name: "Budi", Username: "budi", Role: model.RoleOperator,
		IsActive: true, CreatedAt: t0.Add(-48 * time.Hour),
	}
}

func boss() model.User {
	return model.User{
		ID: 3, Name: "Admin", Username: "admin", Role: model.RoleAdmin,
		IsActive: true, CreatedAt: t0.Add(-72 * time.Hour),
	}
}

func input() operator.Input {
	return operator.Input{Name: "Budi", Username: "budi", Password: "rahasia123"}
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

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Fields
}

// ---------------------------------------------------------------- the boundary

// Every change an admin can make refuses an admin account, and all of them go
// through one guard rather than each checking for itself.
func TestNoAdminAccountCanBeChangedFromTheAdminPanel(t *testing.T) {
	changes := map[string]func(*fixture) error{
		"deactivate": func(f *fixture) error { return f.uc.SetActive(context.Background(), actor, 3, false) },
		"activate":   func(f *fixture) error { return f.uc.SetActive(context.Background(), actor, 3, true) },
		"reset the password": func(f *fixture) error {
			_, err := f.uc.ResetPassword(context.Background(), actor, 3)
			return err
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.users.EXPECT().LockByID(gomock.Any(), int64(3)).Return(boss(), nil)
			// Nothing is written: no SetActive, no SetPassword, no session
			// delete, no log row.

			if got := code(t, change(f)); got != model.CodeConflict {
				t.Errorf("code = %s, want %s", got, model.CodeConflict)
			}
		})
	}
}

// An account that is not there is reported as missing by every change, not as
// something that half happened.
func TestEveryChangeRefusesAnAccountThatIsNotThere(t *testing.T) {
	changes := map[string]func(*fixture) error{
		"deactivate": func(f *fixture) error { return f.uc.SetActive(context.Background(), actor, 404, false) },
		"reset the password": func(f *fixture) error {
			_, err := f.uc.ResetPassword(context.Background(), actor, 404)
			return err
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.users.EXPECT().LockByID(gomock.Any(), int64(404)).Return(model.User{}, model.ErrNoRecord)

			if got := code(t, change(f)); got != model.CodeNotFound {
				t.Errorf("code = %s, want %s", got, model.CodeNotFound)
			}
		})
	}
}

// ---------------------------------------------------------------- create

// The account and its log row commit together (FR-26), and the password never
// appears in the log, not even as a placeholder.
func TestCreateAddsAnOperatorAndItsLogRowTogether(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().Create(gomock.Any(), gomock.Any(), t0).
		DoAndReturn(func(_ context.Context, u model.User, _ time.Time) (int64, error) {
			if u.Name != "Budi" || u.Username != "budi" {
				t.Errorf("user = %+v", u)
			}
			// An admin cannot be made through this door at all.
			if u.Role != model.RoleOperator {
				t.Errorf("role = %s, want %s", u.Role, model.RoleOperator)
			}
			// What is stored is a hash, and it verifies.
			if u.PasswordHash == "rahasia123" {
				t.Fatal("the password was stored in the clear")
			}
			okPassword, err := security.VerifyPassword(u.PasswordHash, "rahasia123")
			if err != nil || !okPassword {
				t.Errorf("the stored hash does not verify: %v", err)
			}
			return 4, nil
		})
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionCreate || e.EntityType != model.EntityUser || e.EntityID != 4 {
				t.Errorf("entry = %+v", e)
			}
			if e.Label != "Budi (budi)" {
				t.Errorf("label = %q, want the name and the username", e.Label)
			}
			if _, leaked := e.After["password"]; leaked {
				t.Error("the log row mentions the password")
			}
			for k, v := range e.After {
				if s, ok := v.(string); ok && strings.Contains(s, "rahasia") {
					t.Errorf("after[%s] carries the password", k)
				}
			}
			return nil
		})

	id, err := f.uc.Create(context.Background(), actor, input())
	if err != nil {
		t.Fatal(err)
	}
	if id != 4 {
		t.Errorf("id = %d, want 4", id)
	}
	if f.unit.Attempts != 1 {
		t.Errorf("opened the unit of work %d times, want 1", f.unit.Attempts)
	}
}

// The username is lower-cased and trimmed, so there is one spelling of each.
func TestCreateNormalizesTheUsername(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, u model.User, _ time.Time) (int64, error) {
			if u.Username != "budi" {
				t.Errorf("username = %q, want budi", u.Username)
			}
			return 4, nil
		})
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)

	in := input()
	in.Username = "  BUDI  "
	if _, err := f.uc.Create(context.Background(), actor, in); err != nil {
		t.Fatal(err)
	}
}

// A username already taken is reported on that field, so the form marks the box.
func TestCreateReportsATakenUsernameOnTheUsernameField(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(0), model.ErrDuplicateID)
	// No log row is written.

	_, err := f.uc.Create(context.Background(), actor, input())
	if got := code(t, err); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
	if fieldsOf(t, err)["username"] == "" {
		t.Error("the refusal must name the username")
	}
}

func TestCreateReportsEveryBadFieldAtOnce(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// Nothing is read: the form is refused before any lock, and before the
	// deliberately slow password hashing.

	_, err := f.uc.Create(context.Background(), actor, operator.Input{
		Name:     "",
		Username: "Budi Santoso", // upper case and a space
		Password: "123",
	})
	if got := code(t, err); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
	got := fieldsOf(t, err)
	for _, field := range []string{"name", "username", "password"} {
		if got[field] == "" {
			t.Errorf("%s is not reported in %v", field, got)
		}
	}
	if f.unit.Attempts != 0 {
		t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
	}
}

// Note that "Budi" is not here: it is lower-cased before it is checked, so it
// is a valid username spelled oddly, which TestCreateNormalizesTheUsername
// covers. A hyphen is not allowed, because the pattern is deliberately narrow.
func TestCreateRefusesABadUsername(t *testing.T) {
	for _, username := range []string{"ab", strings.Repeat("a", 33), "budi-santoso", "budi santoso", "budi!", ""} {
		t.Run(username, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			in := input()
			in.Username = username

			_, err := f.uc.Create(context.Background(), actor, in)
			if fieldsOf(t, err)["username"] == "" {
				t.Errorf("%q should be refused", username)
			}
		})
	}
}

// The limits are allowed, and the password is counted in characters rather than
// bytes, because that is what the person typing counted.
func TestCreateAllowsTheExactLimits(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(int64(4), nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)

	in := operator.Input{
		Name:     strings.Repeat("é", model.MaxUserNameRunes),
		Username: strings.Repeat("a", 32),
		Password: strings.Repeat("é", model.MinOperatorPassword), // 6 characters, 12 bytes
	}
	if _, err := f.uc.Create(context.Background(), actor, in); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- activate

// Deactivating ends their sessions in the same commit, including the one on a
// kiosk they may be standing at.
func TestDeactivatingAnOperatorEndsTheirSessions(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockByID(gomock.Any(), int64(4)).Return(budi(), nil)
	f.users.EXPECT().SetActive(gomock.Any(), int64(4), false).Return(nil)
	f.sessions.EXPECT().DeleteByUser(gomock.Any(), int64(4)).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionDeactivate {
				t.Errorf("action = %s, want %s", e.Action, model.ActionDeactivate)
			}
			if e.Before["isActive"] != true || e.After["isActive"] != false {
				t.Errorf("entry = %v -> %v", e.Before, e.After)
			}
			return nil
		})

	if err := f.uc.SetActive(context.Background(), actor, 4, false); err != nil {
		t.Fatal(err)
	}
}

// Reactivating does not touch sessions: there are none, and the person is not
// owed one.
func TestReactivatingAnOperatorLeavesSessionsAlone(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	off := budi()
	off.IsActive = false
	f.users.EXPECT().LockByID(gomock.Any(), int64(4)).Return(off, nil)
	f.users.EXPECT().SetActive(gomock.Any(), int64(4), true).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionUpdate {
				t.Errorf("action = %s, want %s", e.Action, model.ActionUpdate)
			}
			return nil
		})
	// No DeleteByUser is expected.

	if err := f.uc.SetActive(context.Background(), actor, 4, true); err != nil {
		t.Fatal(err)
	}
}

func TestSetActiveToWhatItAlreadyIsWritesNothing(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().LockByID(gomock.Any(), int64(4)).Return(budi(), nil)
	// No SetActive, no session delete, no log row.

	if err := f.uc.SetActive(context.Background(), actor, 4, true); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- reset

// A reset clears the lockout, because the usual reason for asking is that the
// account is locked out (FR-13), and ends the sessions, because whoever had the
// old password should not stay signed in on the strength of it.
func TestResetPasswordClearsTheLockoutAndTheSessions(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	locked := budi()
	until := t0.Add(5 * time.Minute)
	locked.FailedLogins, locked.LockedUntil = 5, &until

	var hash string
	f.users.EXPECT().LockByID(gomock.Any(), int64(4)).Return(locked, nil)
	// SetPassword is the one that clears failed_logins and locked_until: that
	// is why it is one method and not two.
	f.users.EXPECT().SetPassword(gomock.Any(), int64(4), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ int64, h string) error { hash = h; return nil })
	f.sessions.EXPECT().DeleteByUser(gomock.Any(), int64(4)).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.After["password"] != "(diatur ulang)" {
				t.Errorf("after = %v", e.After)
			}
			return nil
		})

	pw, err := f.uc.ResetPassword(context.Background(), actor, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != 10 {
		t.Errorf("password = %q, want ten characters", pw)
	}
	// What was returned is the password, and what was stored verifies against it.
	if pw == hash {
		t.Fatal("the password was stored in the clear")
	}
	okPassword, err := security.VerifyPassword(hash, pw)
	if err != nil || !okPassword {
		t.Errorf("the stored hash does not verify against the password returned: %v", err)
	}
	// And the password itself is not in the log.
	if strings.Contains(pw, "diatur") {
		t.Error("the fixture is wrong")
	}
}

// Two resets give two different passwords: a predictable one would be no better
// than the lockout it clears.
func TestResetPasswordIsRandomEachTime(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		f, ctrl := newFixture(t)
		f.users.EXPECT().LockByID(gomock.Any(), int64(4)).Return(budi(), nil)
		f.users.EXPECT().SetPassword(gomock.Any(), int64(4), gomock.Any()).Return(nil)
		f.sessions.EXPECT().DeleteByUser(gomock.Any(), int64(4)).Return(nil)
		f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)

		pw, err := f.uc.ResetPassword(context.Background(), actor, 4)
		if err != nil {
			t.Fatal(err)
		}
		if seen[pw] {
			t.Fatalf("the same password came back twice: %q", pw)
		}
		seen[pw] = true
		ctrl.Finish()
	}
}

// ---------------------------------------------------------------- the list

// The visit window is whole local days, not the last 720 hours: "visits this
// month" is something a person counts on a calendar.
func TestListCountsVisitsOverWholeLocalDays(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	lastVisit := t0.Add(-3 * time.Hour)

	f.users.EXPECT().List(gomock.Any()).Return([]model.User{boss(), budi()}, nil)
	// t0 is 22 Oct 10:00 WIB, so the window starts at local midnight on
	// 23 Sep, which is 22 Sep 17:00 UTC.
	f.reports.EXPECT().OperatorVisits(gomock.Any(), time.Date(2026, 9, 22, 17, 0, 0, 0, time.UTC)).
		Return(map[int64]int64{4: 7}, nil)
	f.reports.EXPECT().OperatorLastVisit(gomock.Any()).
		Return(map[int64]model.Visit{4: {At: lastVisit, MachineCode: "VM-01"}}, nil)

	got, err := f.uc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want two accounts, got %d", len(got))
	}
	// Admins are listed too: they can restock, so they belong in the filter.
	if got[0].User.Role != model.RoleAdmin {
		t.Errorf("first = %+v, want the admin listed", got[0].User)
	}
	if got[0].Visits30 != 0 || got[0].Last != nil {
		t.Errorf("admin figures = %+v, want zeroes", got[0])
	}
	if got[1].Visits30 != 7 || got[1].Last == nil || got[1].Last.MachineCode != "VM-01" {
		t.Errorf("operator figures = %+v", got[1])
	}
}

// ---------------------------------------------------------------- the console

// The console is what the command line gets and the API does not. It writes no
// log row, because there is nobody signed in to attribute the change to.
func TestCreateAdminMakesAnAdminAndWritesNoLogRow(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().Create(gomock.Any(), gomock.Any(), t0).
		DoAndReturn(func(_ context.Context, u model.User, _ time.Time) (int64, error) {
			if u.Role != model.RoleAdmin {
				t.Errorf("role = %s, want %s", u.Role, model.RoleAdmin)
			}
			return 3, nil
		})
	// No audit Write, and no unit of work at all.

	id, err := f.uc.CreateAdmin(context.Background(), "Admin", "admin", "admin12345")
	if err != nil {
		t.Fatal(err)
	}
	if id != 3 {
		t.Errorf("id = %d, want 3", id)
	}
	if f.unit.Attempts != 0 {
		t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
	}
}

// An admin password has a higher floor than an operator's: it protects every
// machine's figures rather than one visit.
func TestCreateAdminHoldsPasswordsToTheAdminFloor(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	// Seven characters would be fine for an operator and is not for an admin.
	short := strings.Repeat("a", model.MinAdminPassword-1)
	_, err := f.uc.CreateAdmin(context.Background(), "Admin", "admin", short)
	if fieldsOf(t, err)["password"] == "" {
		t.Error("a short admin password must be refused")
	}
}

func TestCreateAdminReportsATakenUsername(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(0), model.ErrDuplicateID)

	_, err := f.uc.CreateAdmin(context.Background(), "Admin", "admin", "admin12345")
	if got := fieldsOf(t, err)["username"]; !strings.Contains(got, "admin") {
		t.Errorf("fields[username] = %q, want the username named", got)
	}
}

// Setting a password by name reactivates the account, which is what makes this
// the way back in for an admin locked out of their own panel.
func TestConsoleSetPasswordReachesAnAccountByName(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.users.EXPECT().SetPasswordByUsername(gomock.Any(), "admin", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, hash string) error {
			okPassword, err := security.VerifyPassword(hash, "admin12345")
			if err != nil || !okPassword {
				t.Errorf("the stored hash does not verify: %v", err)
			}
			return nil
		})

	if err := f.uc.SetPassword(context.Background(), "admin", "admin12345"); err != nil {
		t.Fatal(err)
	}
}

func TestConsoleSetPasswordRefusesAnUnknownAccountAndAShortPassword(t *testing.T) {
	t.Run("unknown account", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.users.EXPECT().SetPasswordByUsername(gomock.Any(), "hantu", gomock.Any()).
			Return(model.ErrNoRecord)

		err := f.uc.SetPassword(context.Background(), "hantu", "admin12345")
		if got := code(t, err); got != model.CodeNotFound {
			t.Errorf("code = %s, want %s", got, model.CodeNotFound)
		}
	})
	t.Run("short password", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		// Nothing is written: the password is refused before it is hashed.

		err := f.uc.SetPassword(context.Background(), "admin", "short")
		if fieldsOf(t, err)["password"] == "" {
			t.Error("a short password must be refused")
		}
	})
}

// A storage failure is reported as itself, so an outage does not look to the
// admin like a form filled in wrong.
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	boom := errors.New("the users table is unreachable")
	f.users.EXPECT().List(gomock.Any()).Return(nil, boom)

	if _, err := f.uc.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}
