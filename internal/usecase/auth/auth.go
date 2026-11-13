// Package auth decides who is making a request (UC-06, FR-13, FR-23, BR-12,
// BR-13).
//
// Three kinds of caller reach this system, and each is established differently:
//
//	an admin     signs in with a username and password, and gets a session token
//	             that goes idle after 30 minutes (BR-13)
//	an operator  signs in the same way but on one machine, and gets a token that
//	             goes idle after 2 minutes, because the kiosk is a screen in
//	             public (BR-13)
//	an agent      presents its machine's key on every request, which never
//	             expires but can be rotated by an admin (FR-23)
//
// Two rules in here protect against guessing, and both are written so that no
// caller can tell what it got wrong:
//
// An unknown username and a wrong password give the same answer, after the same
// amount of work, so neither reveals whether an account exists (FR-13). Five
// wrong passwords shut an account for five minutes, and while it is shut even
// the right password is refused, so the lock cannot be probed either (TC-19).
package auth

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"vending/internal/common/clock"
	"vending/internal/common/security"
	"vending/internal/model"
	"vending/internal/repository/machine"
	"vending/internal/repository/session"
	"vending/internal/repository/uow"
)

//go:generate mockgen -source=auth.go -destination=mock/auth_mock.go -package=mock

const (
	// maxFailedLogins and lockDuration are FR-13: five tries, then five minutes.
	maxFailedLogins = 5
	lockDuration    = 5 * time.Minute

	// touchEvery is how rarely a session's last-used time is written. Every
	// request would mean a write per request for no gain: the idle timeouts are
	// minutes, so a few seconds of imprecision costs nothing.
	touchEvery = 15 * time.Second

	// machineSeenEvery is the same idea for an agent's contact time (FR-28).
	machineSeenEvery = 30 * time.Second

	// tokenPrefixAdmin and tokenPrefixOperator make a token recognisable in a
	// log or a bug report without revealing it.
	tokenPrefixAdmin    = "adm_"
	tokenPrefixOperator = "opr_"
)

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// AdminLogin signs in to the admin web. An operator account is refused:
	// restocking a machine is not permission to manage the system (TC-21).
	AdminLogin(ctx context.Context, username, password string) (model.Login, error)

	// OperatorLogin starts operator mode on one machine (UC-04). An admin may
	// sign in here too, since an admin can do anything an operator can.
	OperatorLogin(ctx context.Context, m model.Machine, username, password string) (model.Login, error)

	// Logout ends a session. A token that is not there is not an error.
	Logout(ctx context.Context, token string) error

	// AdminSession resolves an admin token to who is calling, or refuses it.
	AdminSession(ctx context.Context, token string) (model.Principal, error)

	// OperatorSession resolves an operator token, which is only good on the
	// machine it was issued for.
	OperatorSession(ctx context.Context, m model.Machine, token string) (model.Principal, error)

	// Machine resolves an agent's machine key to the machine it speaks for, and
	// records the contact (FR-28).
	Machine(ctx context.Context, key string) (model.Machine, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	// uow is for the login check alone: reading the lockout counter and raising
	// it must commit together, or parallel guesses all read the same count.
	uow uow.Repository
	// sessions and machines are pool-bound. Resolving a token happens on every
	// single request, and it changes nothing that another request could race
	// with, so it must not pay for a transaction.
	sessions     session.Repository
	machines     machine.Repository
	clock        clock.Clock
	adminIdle    time.Duration
	operatorIdle time.Duration
	log          *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(u uow.Repository, sessions session.Repository, machines machine.Repository, c clock.Clock, adminIdle, operatorIdle time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		uow: u, sessions: sessions, machines: machines, clock: c,
		adminIdle: adminIdle, operatorIdle: operatorIdle, log: log,
	}
}

// ---------------------------------------------------------------- the refusals

// errInvalidLogin is the answer to an unknown username and to a wrong password
// alike, so the two cannot be told apart (FR-13). extra may carry how many
// tries are left, which is a kindness to the person typing, not information
// about whether the account exists: it is only ever sent once a password was
// actually checked against a real account.
func errInvalidLogin(extra map[string]any) *model.Error {
	e := model.NewError(model.CodeInvalidLogin, "Username atau password salah.")
	e.Extra = extra
	return e
}

// errLocked says when the account reopens. The message rounds up to whole
// minutes because that is what a person can act on; retryInSeconds is exact,
// for the screen's countdown.
func errLocked(left time.Duration) *model.Error {
	return model.Locked(
		"Akun terkunci karena terlalu banyak percobaan. Coba lagi dalam "+
			strconv.Itoa(int(math.Ceil(left.Minutes())))+" menit.",
		map[string]any{"retryInSeconds": int(math.Ceil(left.Seconds()))},
	)
}

// errSessionEnded is the answer for a token that is missing, unknown, idle,
// for the wrong machine, or whose account was deactivated. One answer for all
// of them: the client's move is the same in every case, and saying which it was
// would tell an attacker which tokens exist.
func errSessionEnded() *model.Error {
	return model.Unauthorized("Sesi berakhir. Silakan masuk lagi.")
}

// ---------------------------------------------------------------- signing in

// checkPassword verifies a username and password and applies the lockout rule.
//
// The refusal is carried out of the unit of work in a variable rather than
// returned as an error, because a refusal must still commit: the whole point of
// the counter is that a wrong password is remembered. Returning an error here
// would roll the count back and make the lockout unreachable.
func (s *Service) checkPassword(ctx context.Context, username, password string) (model.User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var out model.User
	var refusal error
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		out, refusal = model.User{}, nil

		u, err := tx.Users().LockForUpdate(ctx, username)
		if errors.Is(err, model.ErrNoRecord) {
			// Spend the same time a real check would, so the answer for an
			// unknown username is not measurably faster (FR-13).
			security.BurnPasswordCheck(password)
			refusal = errInvalidLogin(nil)
			return nil
		}
		if err != nil {
			return err
		}

		now := s.clock.Now()
		if left, locked := u.IsLockedAt(now); locked {
			// Checked before the password, so a locked account tells a guesser
			// nothing about whether the password was right.
			refusal = errLocked(left)
			return nil
		}

		ok, err := security.VerifyPassword(u.PasswordHash, password)
		if err != nil {
			return err
		}
		if !ok {
			failed := u.FailedLogins + 1
			if failed >= maxFailedLogins {
				refusal = errLocked(lockDuration)
				return tx.Users().LockUntil(ctx, u.ID, now.Add(lockDuration))
			}
			refusal = errInvalidLogin(map[string]any{"attemptsLeft": maxFailedLogins - failed})
			return tx.Users().SetFailedLogins(ctx, u.ID, failed)
		}

		// The password was right. Whether the account may be used is a separate
		// question, and answered separately: a deactivated account is told so
		// rather than being left to look like a typo.
		if !u.IsActive {
			refusal = model.Forbidden("Akun ini dinonaktifkan.")
			return nil
		}
		if u.FailedLogins != 0 || u.LockedUntil != nil {
			if err := tx.Users().ClearFailedLogins(ctx, u.ID); err != nil {
				return err
			}
		}
		out = u
		return nil
	})
	if err != nil {
		return model.User{}, err
	}
	if refusal != nil {
		return model.User{}, refusal
	}
	return out, nil
}

// start issues a session token for an account that has just proved itself.
func (s *Service) start(ctx context.Context, u model.User, kind string, machineID *int64) (model.Login, error) {
	prefix := tokenPrefixAdmin
	if kind == model.KindOperator {
		prefix = tokenPrefixOperator
	}
	token := security.NewToken(prefix)
	now := s.clock.Now()
	err := s.sessions.Create(ctx, model.Session{
		UserID:    u.ID,
		Kind:      kind,
		TokenHash: security.HashToken(token),
		MachineID: machineID,
		CreatedAt: now,
		// A new session counts as just used, or it would start out idle.
		LastSeenAt: now,
	})
	if err != nil {
		return model.Login{}, err
	}
	return model.Login{Token: token, Name: u.Name, Username: u.Username}, nil
}

func (s *Service) AdminLogin(ctx context.Context, username, password string) (model.Login, error) {
	u, err := s.checkPassword(ctx, username, password)
	if err != nil {
		return model.Login{}, err
	}
	if u.Role != model.RoleAdmin {
		return model.Login{}, model.Forbidden("Akun operator tidak bisa masuk ke panel admin.")
	}
	return s.start(ctx, u, model.KindAdmin, nil)
}

func (s *Service) OperatorLogin(ctx context.Context, m model.Machine, username, password string) (model.Login, error) {
	u, err := s.checkPassword(ctx, username, password)
	if err != nil {
		return model.Login{}, err
	}
	// No role check: an admin restocking a machine is expected.
	machineID := m.ID
	return s.start(ctx, u, model.KindOperator, &machineID)
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return s.sessions.DeleteByTokenHash(ctx, security.HashToken(token))
}

// ---------------------------------------------------------------- staying in

func (s *Service) AdminSession(ctx context.Context, token string) (model.Principal, error) {
	return s.resolve(ctx, token, model.KindAdmin, 0, s.adminIdle)
}

func (s *Service) OperatorSession(ctx context.Context, m model.Machine, token string) (model.Principal, error) {
	return s.resolve(ctx, token, model.KindOperator, m.ID, s.operatorIdle)
}

// resolve turns a token into the principal a request acts as, or refuses it.
//
// A token that is refused is also deleted. That is deliberate: once a session
// has gone idle or its account has been deactivated, the token is worthless,
// and leaving the row behind means the next request has to decide all over
// again. machineID is 0 for an admin session, which belongs to no machine.
func (s *Service) resolve(ctx context.Context, token, kind string, machineID int64, idle time.Duration) (model.Principal, error) {
	if token == "" {
		return model.Principal{}, errSessionEnded()
	}
	a, err := s.sessions.ByTokenHash(ctx, kind, security.HashToken(token))
	if errors.Is(err, model.ErrNoRecord) {
		return model.Principal{}, errSessionEnded()
	}
	if err != nil {
		return model.Principal{}, err
	}

	now := s.clock.Now()
	valid := a.UserActive && now.Sub(a.LastSeenAt) <= idle
	if kind == model.KindAdmin && a.Principal.Role != model.RoleAdmin {
		// The account was demoted while signed in.
		valid = false
	}
	if kind == model.KindOperator && a.Principal.MachineID != machineID {
		// An operator token is good on the machine it was issued for and
		// nowhere else, so one kiosk's token cannot restock another.
		valid = false
	}
	if !valid {
		if err := s.sessions.Delete(ctx, a.Principal.SessionID); err != nil {
			// Worth knowing about, but not worth failing the request: the
			// session is refused either way.
			s.log.Warn("could not delete an ended session", "session", a.Principal.SessionID, "err", err)
		}
		return model.Principal{}, errSessionEnded()
	}

	if now.Sub(a.LastSeenAt) >= touchEvery {
		if err := s.sessions.TouchSeen(ctx, a.Principal.SessionID, now); err != nil {
			return model.Principal{}, err
		}
	}
	return a.Principal, nil
}

// Machine resolves an agent's machine key (FR-23, BR-12) and records the
// contact time the recap shows as "kontak terakhir" (FR-28).
func (s *Service) Machine(ctx context.Context, key string) (model.Machine, error) {
	unknown := model.NewError(model.CodeInvalidMachineKey, "Kunci mesin tidak dikenal.")
	if key == "" {
		return model.Machine{}, unknown
	}
	rec, err := s.machines.ByAPIKeyHash(ctx, security.HashToken(key))
	if errors.Is(err, model.ErrNoRecord) {
		return model.Machine{}, unknown
	}
	if err != nil {
		return model.Machine{}, err
	}
	// A deactivated machine is told apart from an unknown key: the agent is
	// genuine, so its operator deserves to know why it has stopped selling.
	if !rec.IsActive {
		return model.Machine{}, model.MachineInactive("Mesin ini dinonaktifkan di panel admin.")
	}
	if now := s.clock.Now(); rec.LastSeenAt == nil || now.Sub(*rec.LastSeenAt) >= machineSeenEvery {
		if err := s.machines.TouchSeen(ctx, rec.ID, now); err != nil {
			return model.Machine{}, err
		}
	}
	return rec.Identity(), nil
}
