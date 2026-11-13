// Package operator owns the staff accounts an admin manages (UC-12).
//
// The boundary this package defends is which accounts an admin may touch. An
// admin manages operators and nothing else: admin accounts are made and
// repaired from the command line, on the machine the server runs on, by
// somebody with shell access. That is deliberate, and it is the reason every
// change here goes through one guard rather than each one checking for itself.
// An admin who could edit admins could quietly promote themselves, and the
// change log would record it as an ordinary edit.
//
// Two of these actions end sessions, and both do it in the same commit as the
// change: deactivating an account, and resetting its password. In each case the
// person holding the old token should stop being trusted at once rather than
// whenever it next goes idle.
package operator

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"vending/internal/common/calendar"
	"vending/internal/common/clock"
	"vending/internal/common/security"
	"vending/internal/model"
	"vending/internal/repository/report"
	"vending/internal/repository/uow"
	"vending/internal/repository/user"
)

//go:generate mockgen -source=operator.go -destination=mock/operator_mock.go -package=mock

// usernamePattern is what a username may be: lower case, so there is one
// spelling of each, and no characters that are hard to type on a kiosk.
var usernamePattern = regexp.MustCompile(`^[a-z0-9._]{3,32}$`)

// visitWindowDays is how far back the "visits" figure on the list looks.
const visitWindowDays = 30

// temporaryPasswordLength is how long a reset password is. It is read off a
// screen and typed on a kiosk, so it is short enough to be, and random enough
// not to be guessed in the five tries the lockout allows.
const temporaryPasswordLength = 10

// Input is an operator an admin is adding.
type Input struct {
	Name     string
	Username string
	Password string
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// List returns every staff account with its visit figures (UC-12).
	List(ctx context.Context) ([]model.OperatorDetail, error)
	// Create adds an operator account.
	Create(ctx context.Context, actor model.Principal, in Input) (int64, error)
	// SetActive deactivates or reactivates an operator.
	SetActive(ctx context.Context, actor model.Principal, id int64, active bool) error
	// ResetPassword sets a random temporary password and returns it once.
	ResetPassword(ctx context.Context, actor model.Principal, id int64) (string, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	uow uow.Repository
	// users and reports are pool-bound, for the reads that change nothing.
	users   user.Repository
	reports report.Repository
	clock   clock.Clock
	// loc is the zone whose calendar days the visit window counts.
	loc *time.Location
	log *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(u uow.Repository, users user.Repository, reports report.Repository, c clock.Clock, loc *time.Location, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uow: u, users: users, reports: reports, clock: c, loc: loc, log: log}
}

// label is how an account appears in the change log: the name to recognise and
// the username to identify, since names are not unique and usernames are.
func label(u model.User) string { return u.Name + " (" + u.Username + ")" }

// List returns the staff accounts with their visit figures.
func (s *Service) List(ctx context.Context) ([]model.OperatorDetail, error) {
	users, err := s.users.List(ctx)
	if err != nil {
		return nil, err
	}
	// The window is whole local days, not the last 720 hours: "visits this
	// month" is a thing somebody counts on a calendar.
	since := calendar.StartOfDay(s.clock.Now().In(s.loc)).AddDate(0, 0, -(visitWindowDays - 1)).UTC()
	visits, err := s.reports.OperatorVisits(ctx, since)
	if err != nil {
		return nil, err
	}
	last, err := s.reports.OperatorLastVisit(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]model.OperatorDetail, 0, len(users))
	for _, u := range users {
		d := model.OperatorDetail{User: u, Visits30: visits[u.ID]}
		if v, ok := last[u.ID]; ok {
			visit := v
			d.Last = &visit
		}
		out = append(out, d)
	}
	return out, nil
}

// clean checks what an admin typed, adding a message per field at fault.
func (in Input) clean(fe model.FieldErrors) (name, username string) {
	name = strings.TrimSpace(in.Name)
	username = strings.ToLower(strings.TrimSpace(in.Username))

	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe.Add("name", "Nama wajib diisi.")
	case n > model.MaxUserNameRunes:
		fe.Add("name", "Nama paling panjang 100 karakter.")
	}
	if !usernamePattern.MatchString(username) {
		fe.Add("username", "3–32 karakter: huruf kecil, angka, titik atau garis bawah.")
	}
	// The password is measured in characters, not bytes, because that is what
	// the person typing counted.
	switch n := utf8.RuneCountInString(in.Password); {
	case n < model.MinOperatorPassword:
		fe.Add("password", "Paling sedikit 6 karakter.")
	case n > model.MaxPassword:
		fe.Add("password", "Paling panjang 128 karakter.")
	}
	return name, username
}

// Create adds an operator account.
func (s *Service) Create(ctx context.Context, actor model.Principal, in Input) (int64, error) {
	fe := model.FieldErrors{}
	name, username := in.clean(fe)
	if err := fe.Err(); err != nil {
		return 0, err
	}

	// Hashing is deliberate work — argon2id, tens of milliseconds — so it
	// happens before the unit of work opens rather than while holding a lock.
	hash, err := security.HashPassword(in.Password)
	if err != nil {
		return 0, err
	}

	var id int64
	err = s.uow.Do(ctx, func(tx uow.Tx) error {
		id = 0
		now := s.clock.Now()
		var err error
		id, err = tx.Users().Create(ctx, model.User{
			Name: name, Username: username, PasswordHash: hash, Role: model.RoleOperator,
		}, now)
		if errors.Is(err, model.ErrDuplicateID) {
			// On the field the admin typed, so the form marks the box.
			return model.FieldErrors{"username": "Username sudah dipakai."}.Err()
		}
		if err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionCreate,
			EntityType: model.EntityUser, EntityID: id, Label: label(model.User{Name: name, Username: username}),
			// The password is not in the log, not even as a placeholder: there
			// is nothing about it a reader needs and everything to lose.
			After: map[string]any{"name": name, "username": username, "role": model.RoleOperator},
			At:    now,
		})
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// manageable loads an account for a change an admin is making, and refuses an
// admin account.
//
// This is the guard the package comment describes, in one place rather than
// repeated: an admin who could edit admins could promote themselves, and the
// log would show it as an ordinary edit.
func manageable(ctx context.Context, tx uow.Tx, id int64) (model.User, error) {
	u, err := tx.Users().LockByID(ctx, id)
	if errors.Is(err, model.ErrNoRecord) {
		return model.User{}, model.NotFound("Akun tidak ditemukan.")
	}
	if err != nil {
		return model.User{}, err
	}
	if u.Role == model.RoleAdmin {
		return model.User{}, model.Conflict(model.CodeConflict, "Akun admin tidak bisa diubah dari sini.", nil)
	}
	return u, nil
}

// SetActive deactivates or reactivates an operator.
//
// Deactivating ends their sessions in the same commit, including the one on a
// kiosk they may be standing at. Setting it to what it already is changes
// nothing.
func (s *Service) SetActive(ctx context.Context, actor model.Principal, id int64, active bool) error {
	return s.uow.Do(ctx, func(tx uow.Tx) error {
		u, err := manageable(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.IsActive == active {
			return nil
		}
		if err := tx.Users().SetActive(ctx, id, active); err != nil {
			return err
		}
		if !active {
			if err := tx.Sessions().DeleteByUser(ctx, id); err != nil {
				return err
			}
		}
		action := model.ActionUpdate
		if !active {
			action = model.ActionDeactivate
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: action,
			EntityType: model.EntityUser, EntityID: id, Label: label(u),
			Before: map[string]any{"isActive": !active},
			After:  map[string]any{"isActive": active},
			At:     s.clock.Now(),
		})
	})
}

// ResetPassword sets a random temporary password, returns it once, and clears
// both the lockout and every session of that account.
//
// The lockout is cleared because a new password should not arrive already shut
// out by the old one's failed attempts — which is the usual reason a reset is
// being asked for (FR-13). The sessions go because whoever was using the old
// password should not stay signed in on the strength of it.
func (s *Service) ResetPassword(ctx context.Context, actor model.Principal, id int64) (string, error) {
	password := security.TemporaryPassword(temporaryPasswordLength)
	hash, err := security.HashPassword(password)
	if err != nil {
		return "", err
	}
	err = s.uow.Do(ctx, func(tx uow.Tx) error {
		u, err := manageable(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := tx.Users().SetPassword(ctx, id, hash); err != nil {
			return err
		}
		if err := tx.Sessions().DeleteByUser(ctx, id); err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionUpdate,
			EntityType: model.EntityUser, EntityID: id, Label: label(u),
			// That it was reset, never the password itself or the old one's
			// hash: this row is read by people.
			Before: map[string]any{"password": "••••"},
			After:  map[string]any{"password": "(diatur ulang)"},
			At:     s.clock.Now(),
		})
	})
	if err != nil {
		return "", err
	}
	return password, nil
}

// ---------------------------------------------------------------- the console

// Console is the account management the command line needs and the API must
// not have.
//
// It is a separate port, not two more methods on UseCase, so that the boundary
// the package comment describes is structural: internal/app hands UseCase to
// the delivery layer and Console only to cmd, which means no HTTP handler can
// reach these even by mistake. Neither writes a change-log row, because there
// is nobody signed in to attribute the change to — the audit trail for these is
// shell access to the server.
type Console interface {
	// CreateAdmin adds an admin account.
	CreateAdmin(ctx context.Context, name, username, password string) (int64, error)
	// SetPassword replaces an account's password by username, and reactivates
	// it: this is the way back in for an admin locked out of their own panel.
	SetPassword(ctx context.Context, username, password string) error
}

var _ Console = (*Service)(nil)

// CreateAdmin adds an admin account.
//
// The messages here are in English, unlike the rest of this package: they are
// read on a terminal by whoever is installing the server, not on a screen by
// the people using it. An admin password has a higher floor than an operator's,
// because it protects every machine's figures rather than one visit.
func (s *Service) CreateAdmin(ctx context.Context, name, username, password string) (int64, error) {
	fe := model.FieldErrors{}
	name = strings.TrimSpace(name)
	username = strings.ToLower(strings.TrimSpace(username))

	if n := utf8.RuneCountInString(name); n == 0 || n > model.MaxUserNameRunes {
		fe.Add("name", "name is required (at most 100 characters)")
	}
	if !usernamePattern.MatchString(username) {
		fe.Add("username", "3-32 characters: lowercase letters, digits, dot or underscore")
	}
	if n := utf8.RuneCountInString(password); n < model.MinAdminPassword || n > model.MaxPassword {
		fe.Add("password", "8 to 128 characters")
	}
	if err := fe.Err(); err != nil {
		return 0, err
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return 0, err
	}

	id, err := s.users.Create(ctx, model.User{
		Name: name, Username: username, PasswordHash: hash, Role: model.RoleAdmin,
	}, s.clock.Now())
	if errors.Is(err, model.ErrDuplicateID) {
		return 0, model.FieldErrors{"username": "username " + username + " already exists"}.Err()
	}
	return id, err
}

// SetPassword replaces an account's password by username.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	if n := utf8.RuneCountInString(password); n < model.MinAdminPassword || n > model.MaxPassword {
		return model.FieldErrors{"password": "8 to 128 characters"}.Err()
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	err = s.users.SetPasswordByUsername(ctx, username, hash)
	if errors.Is(err, model.ErrNoRecord) {
		return model.NotFound("no account named " + username)
	}
	return err
}
