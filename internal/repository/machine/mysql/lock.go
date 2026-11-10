package mysql

import (
	"context"
	"database/sql"
	"errors"

	"vending/internal/model"
)

func (s *MySQL) LockShared(ctx context.Context, id int64) (model.MachineRecord, error) {
	// LOCK IN SHARE MODE rather than FOR UPDATE: a purchase only reads the
	// machine, and two purchases on different slots of one machine must not
	// queue behind each other on this row.
	m, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM machines WHERE id = ? LOCK IN SHARE MODE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.MachineRecord{}, model.ErrNoRecord
	}
	return m, err
}

func (s *MySQL) BumpCatalogVersion(ctx context.Context, id int64) error {
	_, err := s.q.ExecContext(ctx, `UPDATE machines SET catalog_version = catalog_version + 1 WHERE id = ?`, id)
	return err
}
