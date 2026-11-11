// Package mysql is the sessions driver.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/session"
)

// MySQL reads and writes the sessions table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ session.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool or one transaction.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

func (s *MySQL) Create(ctx context.Context, sess model.Session) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO sessions
		(token_hash, user_id, kind, machine_id, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, sess.Kind, sess.MachineID, sess.CreatedAt, sess.LastSeenAt)
	return err
}

func (s *MySQL) ByTokenHash(ctx context.Context, kind, tokenHash string) (model.AuthSession, error) {
	var a model.AuthSession
	var machineID sql.NullInt64
	err := s.q.QueryRowContext(ctx, `
		SELECT s.id, s.user_id, s.machine_id, s.created_at, s.last_seen_at,
		       u.name, u.username, u.role, u.is_active
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.kind = ?`, tokenHash, kind).
		Scan(&a.Principal.SessionID, &a.Principal.UserID, &machineID,
			&a.Principal.StartedAt, &a.LastSeenAt,
			&a.Principal.Name, &a.Principal.Username, &a.Principal.Role, &a.UserActive)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AuthSession{}, model.ErrNoRecord
	}
	// Zero for an admin session, which belongs to no machine.
	a.Principal.MachineID = machineID.Int64
	return a, err
}

func (s *MySQL) TouchSeen(ctx context.Context, id int64, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, at, id)
	return err
}

func (s *MySQL) Delete(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

func (s *MySQL) DeleteByTokenHash(ctx context.Context, tokenHash string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *MySQL) DeleteByMachine(ctx context.Context, machineID int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM sessions WHERE machine_id = ?`, machineID)
	return err
}

func (s *MySQL) DeleteByUser(ctx context.Context, userID int64) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}
