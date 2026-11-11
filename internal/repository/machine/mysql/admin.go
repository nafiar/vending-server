package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
)

// The writes an admin makes to a machine, and the reads its screens need.

func (s *MySQL) List(ctx context.Context, id int64) ([]model.MachineRecord, error) {
	q := `SELECT ` + columns + ` FROM machines`
	var args []any
	if id != 0 {
		q += ` WHERE id = ?`
		args = append(args, id)
	}
	rows, err := s.q.QueryContext(ctx, q+` ORDER BY code`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MachineRecord{}
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *MySQL) ByCode(ctx context.Context, code string) (model.MachineRecord, error) {
	m, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE code = ?`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return model.MachineRecord{}, model.ErrNoRecord
	}
	return m, err
}

func (s *MySQL) Create(ctx context.Context, m model.MachineRecord, keyHash string) (int64, error) {
	res, err := s.q.ExecContext(ctx, `INSERT INTO machines
		(code, name, location, api_key_hash, catalog_version, is_active, created_at)
		VALUES (?, ?, ?, ?, 1, TRUE, ?)`,
		m.Code, m.Name, mysqldb.StrOrNil(strings.TrimSpace(m.Location)), keyHash, m.CreatedAt)
	if err != nil {
		if mysqldb.IsDuplicateKey(err) {
			return 0, model.ErrDuplicateID
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *MySQL) SetActive(ctx context.Context, id int64, active bool) error {
	_, err := s.q.ExecContext(ctx, `UPDATE machines SET is_active = ? WHERE id = ?`, active, id)
	return err
}

func (s *MySQL) SetAPIKeyHash(ctx context.Context, id int64, keyHash string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE machines SET api_key_hash = ? WHERE id = ?`, keyHash, id)
	return err
}
