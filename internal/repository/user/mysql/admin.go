package mysql

import (
	"context"
	"errors"
	"strings"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
)

// The reads and writes an admin, or the command line, makes to an account.

func (s *MySQL) List(ctx context.Context) ([]model.User, error) {
	// By role then name: admins first, which is the order the admin's list is
	// shown in.
	rows, err := s.q.QueryContext(ctx, `SELECT `+columns+` FROM users ORDER BY role, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		u, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *MySQL) LockByID(ctx context.Context, id int64) (model.User, error) {
	u, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM users WHERE id = ? FOR UPDATE`, id))
	if errors.Is(err, mysqldb.ErrNoRows) {
		return model.User{}, model.ErrNoRecord
	}
	return u, err
}

func (s *MySQL) Create(ctx context.Context, u model.User, at time.Time) (int64, error) {
	res, err := s.q.ExecContext(ctx, `INSERT INTO users
		(name, username, password_hash, role, is_active, created_at)
		VALUES (?, ?, ?, ?, TRUE, ?)`,
		u.Name, u.Username, u.PasswordHash, u.Role, at)
	if err != nil {
		if mysqldb.IsDuplicateKey(err) {
			return 0, model.ErrDuplicateID
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *MySQL) SetActive(ctx context.Context, id int64, active bool) error {
	_, err := s.q.ExecContext(ctx, `UPDATE users SET is_active = ? WHERE id = ?`, active, id)
	return err
}

func (s *MySQL) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.q.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, failed_logins = 0, locked_until = NULL WHERE id = ?`, hash, id)
	return err
}

func (s *MySQL) SetPasswordByUsername(ctx context.Context, username, hash string) error {
	res, err := s.q.ExecContext(ctx, `UPDATE users
		SET password_hash = ?, failed_logins = 0, locked_until = NULL, is_active = TRUE
		WHERE username = ?`, hash, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNoRecord
	}
	return nil
}
