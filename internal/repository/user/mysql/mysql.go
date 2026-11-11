// Package mysql is the users driver.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/user"
)

// MySQL reads and writes the users table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ user.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool (a direct read) or
// one transaction (a call inside a unit of work).
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

const columns = `id, name, username, password_hash, role, is_active, failed_logins, locked_until`

func scan(r mysqldb.RowScanner) (model.User, error) {
	var u model.User
	var locked sql.NullTime
	err := r.Scan(&u.ID, &u.Name, &u.Username, &u.PasswordHash, &u.Role, &u.IsActive, &u.FailedLogins, &locked)
	if locked.Valid {
		t := locked.Time
		u.LockedUntil = &t
	}
	return u, err
}

func (s *MySQL) LockForUpdate(ctx context.Context, username string) (model.User, error) {
	u, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM users WHERE username = ? FOR UPDATE`, username))
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, model.ErrNoRecord
	}
	return u, err
}

func (s *MySQL) SetFailedLogins(ctx context.Context, id int64, failed int) error {
	_, err := s.q.ExecContext(ctx, `UPDATE users SET failed_logins = ?, locked_until = NULL WHERE id = ?`, failed, id)
	return err
}

func (s *MySQL) LockUntil(ctx context.Context, id int64, until time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = ? WHERE id = ?`, until, id)
	return err
}

func (s *MySQL) ClearFailedLogins(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL WHERE id = ?`, id)
	return err
}

func (s *MySQL) FirstActiveAdminID(ctx context.Context) (int64, error) {
	var id int64
	err := s.q.QueryRowContext(ctx,
		`SELECT id FROM users WHERE role = ? AND is_active ORDER BY id LIMIT 1`, model.RoleAdmin).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, model.ErrNoRecord
	}
	return id, err
}
