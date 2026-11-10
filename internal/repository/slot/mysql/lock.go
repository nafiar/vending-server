package mysql

import (
	"context"
	"database/sql"
	"errors"

	"vending/internal/model"
)

func (s *MySQL) LockForUpdate(ctx context.Context, machineID int64, slotNumber int) (model.Slot, error) {
	// The lock is taken on the plain slots row first, and only then is the
	// joined row read: MySQL locks every row a FOR UPDATE query joined, so
	// locking through listQuery would lock the two products as well and let one
	// purchase block another that merely sells the same product elsewhere.
	var id int64
	err := s.q.QueryRowContext(ctx, `SELECT id FROM slots WHERE machine_id = ? AND slot_number = ? FOR UPDATE`,
		machineID, slotNumber).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Slot{}, model.ErrNoRecord
	}
	if err != nil {
		return model.Slot{}, err
	}
	sl, err := scanSlot(s.q.QueryRowContext(ctx, byIDQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Slot{}, model.ErrNoRecord
	}
	return sl, err
}

func (s *MySQL) TakeOne(ctx context.Context, slotID int64) (bool, error) {
	// The qty > 0 guard keeps the count from going negative even if two
	// dispenses of the same last item were somehow both reported.
	res, err := s.q.ExecContext(ctx, `UPDATE slots SET qty = qty - 1 WHERE id = ? AND qty > 0`, slotID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *MySQL) MarkNeedsCheck(ctx context.Context, slotID int64) error {
	_, err := s.q.ExecContext(ctx, `UPDATE slots SET needs_check = TRUE WHERE id = ?`, slotID)
	return err
}
