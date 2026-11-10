// Package mysql is the machines driver.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"vending/internal/model"
	"vending/internal/repository/machine"
	"vending/internal/repository/mysqldb"
)

// MySQL reads and writes the machines table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ machine.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool (a direct read) or
// one transaction (a call inside a unit of work).
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

const columns = `id, code, name, location, is_active, catalog_version, created_at, last_seen_at`

func scan(r mysqldb.RowScanner) (model.MachineRecord, error) {
	var m model.MachineRecord
	// location is nullable, and created_at is read as nullable because the
	// demo seed writes rows directly.
	var loc sql.NullString
	var created, seen sql.NullTime
	err := r.Scan(&m.ID, &m.Code, &m.Name, &loc, &m.IsActive, &m.CatalogVersion, &created, &seen)
	m.Location = loc.String
	m.CreatedAt = created.Time
	if seen.Valid {
		t := seen.Time
		m.LastSeenAt = &t
	}
	return m, err
}

func (s *MySQL) Get(ctx context.Context, id int64) (model.MachineRecord, error) {
	m, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.MachineRecord{}, model.ErrNoRecord
	}
	return m, err
}

func (s *MySQL) ByAPIKeyHash(ctx context.Context, keyHash string) (model.MachineRecord, error) {
	m, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE api_key_hash = ?`, keyHash))
	if errors.Is(err, sql.ErrNoRows) {
		return model.MachineRecord{}, model.ErrNoRecord
	}
	return m, err
}

func (s *MySQL) TouchSeen(ctx context.Context, id int64, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE machines SET last_seen_at = ? WHERE id = ?`, at, id)
	return err
}
