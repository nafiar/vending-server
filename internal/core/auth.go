package core

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vending/internal/security"
)

const (
	roleAdmin    = "ADMIN"
	roleOperator = "OPERATOR"

	kindAdmin    = "ADMIN"
	kindOperator = "OPERATOR"

	maxFailedLogins = 5               // FR-13
	lockDuration    = 5 * time.Minute // FR-13
	touchEvery      = 15 * time.Second
)

// Principal is a logged-in staff member, taken from a session token.
type Principal struct {
	SessionID int64
	UserID    int64
	Name      string
	Username  string
	Role      string
	MachineID int64     // operator sessions only: the machine the operator logged in on
	StartedAt time.Time // when the session began; for an operator, the start of the visit
}

// Machine is the Kiosk Agent of one machine, taken from its machine key.
type Machine struct {
	ID   int64
	Code string
	Name string
}

type authUser struct {
	id          int64
	name        string
	username    string
	hash        string
	role        string
	active      bool
	failed      int
	lockedUntil sql.NullTime
}

func lockedErr(left time.Duration) *Error {
	secs := int(math.Ceil(left.Seconds()))
	mins := int(math.Ceil(left.Minutes()))
	return &Error{
		Status:  http.StatusLocked,
		Code:    "LOCKED",
		Message: "Akun terkunci karena terlalu banyak percobaan. Coba lagi dalam " + strconv.Itoa(mins) + " menit.",
		Extra:   map[string]any{"retryInSeconds": secs},
	}
}

// checkLogin verifies a username and password and applies the lockout rule:
// five wrong passwords lock the account for five minutes, and during the lock
// even the right password is refused (FR-13, TC-19). The counter is updated
// in its own committed transaction, so failed attempts are remembered.
func (s *Service) checkLogin(ctx context.Context, username, password string) (authUser, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var result authUser
	var refusal error
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		refusal = nil
		var u authUser
		err := tx.QueryRowContext(ctx, `
			SELECT id, name, username, password_hash, role, is_active, failed_logins, locked_until
			FROM users WHERE username = ? FOR UPDATE`, username).
			Scan(&u.id, &u.name, &u.username, &u.hash, &u.role, &u.active, &u.failed, &u.lockedUntil)
		if errors.Is(err, sql.ErrNoRows) {
			security.BurnPasswordCheck(password) // same timing as a real check
			refusal = &Error{Status: http.StatusUnauthorized, Code: "INVALID_LOGIN", Message: "Username atau password salah."}
			return nil
		}
		if err != nil {
			return err
		}
		now := s.now()
		if u.lockedUntil.Valid && u.lockedUntil.Time.After(now) {
			refusal = lockedErr(u.lockedUntil.Time.Sub(now))
			return nil
		}
		ok, err := security.VerifyPassword(u.hash, password)
		if err != nil {
			return err
		}
		if !ok {
			failed := u.failed + 1
			if failed >= maxFailedLogins {
				_, err = tx.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = ? WHERE id = ?`, now.Add(lockDuration), u.id)
				refusal = lockedErr(lockDuration)
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE users SET failed_logins = ?, locked_until = NULL WHERE id = ?`, failed, u.id)
				refusal = &Error{Status: http.StatusUnauthorized, Code: "INVALID_LOGIN", Message: "Username atau password salah.",
					Extra: map[string]any{"attemptsLeft": maxFailedLogins - failed}}
			}
			return err
		}
		if !u.active {
			refusal = errForbidden("Akun ini dinonaktifkan.")
			return nil
		}
		if u.failed != 0 || u.lockedUntil.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL WHERE id = ?`, u.id); err != nil {
				return err
			}
		}
		result = u
		return nil
	})
	if err != nil {
		return authUser{}, err
	}
	if refusal != nil {
		return authUser{}, refusal
	}
	return result, nil
}

func (s *Service) createSession(ctx context.Context, userID int64, kind string, machineID *int64) (string, error) {
	prefix := "adm_"
	if kind == kindOperator {
		prefix = "opr_"
	}
	token := security.NewToken(prefix)
	now := s.now()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, kind, machine_id, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)`, security.HashToken(token), userID, kind, machineID, now, now)
	return token, err
}

// AdminLogin signs in to the admin web. Operator accounts are refused (TC-21).
func (s *Service) AdminLogin(ctx context.Context, username, password string) (LoginResult, error) {
	u, err := s.checkLogin(ctx, username, password)
	if err != nil {
		return LoginResult{}, err
	}
	if u.role != roleAdmin {
		return LoginResult{}, errForbidden("Akun operator tidak bisa masuk ke panel admin.")
	}
	token, err := s.createSession(ctx, u.id, kindAdmin, nil)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, Name: u.name, Username: u.username}, nil
}

// OperatorLogin starts operator mode on one machine (UC-04). Admins may log in
// too, since an admin can do everything an operator can.
func (s *Service) OperatorLogin(ctx context.Context, m Machine, username, password string) (LoginResult, error) {
	u, err := s.checkLogin(ctx, username, password)
	if err != nil {
		return LoginResult{}, err
	}
	token, err := s.createSession(ctx, u.id, kindOperator, &m.ID)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, Name: u.name, Username: u.username}, nil
}

// Logout ends a session. An unknown token is not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, security.HashToken(token))
	return err
}

// AdminSession checks an admin token: it must exist, be used within the last
// 30 minutes (BR-13) and belong to an active admin.
func (s *Service) AdminSession(ctx context.Context, token string) (Principal, error) {
	return s.session(ctx, token, kindAdmin, 0, s.adminIdle)
}

// OperatorSession checks an operator token for this machine (2-minute idle, BR-13).
func (s *Service) OperatorSession(ctx context.Context, m Machine, token string) (Principal, error) {
	return s.session(ctx, token, kindOperator, m.ID, s.operatorIdle)
}

func (s *Service) session(ctx context.Context, token, kind string, machineID int64, idle time.Duration) (Principal, error) {
	expired := errUnauthorized("Sesi berakhir. Silakan masuk lagi.")
	if token == "" {
		return Principal{}, expired
	}
	var p Principal
	var mid sql.NullInt64
	var lastSeen time.Time
	var active bool
	err := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.user_id, s.machine_id, s.created_at, s.last_seen_at, u.name, u.username, u.role, u.is_active
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.kind = ?`, security.HashToken(token), kind).
		Scan(&p.SessionID, &p.UserID, &mid, &p.StartedAt, &lastSeen, &p.Name, &p.Username, &p.Role, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, expired
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now()
	valid := active && now.Sub(lastSeen) <= idle
	if kind == kindAdmin && p.Role != roleAdmin {
		valid = false
	}
	if kind == kindOperator && (!mid.Valid || mid.Int64 != machineID) {
		valid = false
	}
	if !valid {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, p.SessionID)
		return Principal{}, expired
	}
	p.MachineID = mid.Int64
	if now.Sub(lastSeen) >= touchEvery {
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now, p.SessionID); err != nil {
			return Principal{}, err
		}
	}
	return p, nil
}

// Me describes the signed-in admin.
func (s *Service) Me(p Principal) Me { return Me{Name: p.Name, Username: p.Username, Role: p.Role} }

// AuthenticateMachine checks a machine key (FR-23, BR-12) and records the
// contact time that the recap shows as "kontak terakhir" (FR-28).
func (s *Service) AuthenticateMachine(ctx context.Context, key string) (Machine, error) {
	unknown := &Error{Status: http.StatusUnauthorized, Code: "INVALID_MACHINE_KEY", Message: "Kunci mesin tidak dikenal."}
	if key == "" {
		return Machine{}, unknown
	}
	var m Machine
	var active bool
	var lastSeen sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id, code, name, is_active, last_seen_at FROM machines WHERE api_key_hash = ?`,
		security.HashToken(key)).Scan(&m.ID, &m.Code, &m.Name, &active, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, unknown
	}
	if err != nil {
		return Machine{}, err
	}
	if !active {
		return Machine{}, &Error{Status: http.StatusForbidden, Code: "MACHINE_INACTIVE", Message: "Mesin ini dinonaktifkan di panel admin."}
	}
	if now := s.now(); !lastSeen.Valid || now.Sub(lastSeen.Time) >= 30*time.Second {
		if _, err := s.db.ExecContext(ctx, `UPDATE machines SET last_seen_at = ? WHERE id = ?`, now, m.ID); err != nil {
			return Machine{}, err
		}
	}
	return m, nil
}
