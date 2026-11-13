package mysql

import (
	"context"
	"database/sql"
	"errors"

	"strings"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
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

func (s *MySQL) LockAllByMachine(ctx context.Context, machineID int64) ([]model.Slot, error) {
	// The lock is taken on the plain slots rows first, in slot order, for the
	// reason LockForUpdate gives: a FOR UPDATE over the product joins would
	// lock products too. The ordered id read is what fixes the lock order.
	rows, err := s.q.QueryContext(ctx,
		`SELECT id FROM slots WHERE machine_id = ? ORDER BY slot_number FOR UPDATE`, machineID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []model.Slot{}, nil
	}
	// Now the full rows, which the locks above already hold.
	return s.listByIDs(ctx, ids)
}

func (s *MySQL) listByIDs(ctx context.Context, ids []int64) ([]model.Slot, error) {
	rows, err := s.q.QueryContext(ctx,
		byIDsQuery+mysqldb.Placeholders(len(ids))+`) ORDER BY s.slot_number`, mysqldb.Args(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Slot, 0, len(ids))
	for rows.Next() {
		sl, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

func (s *MySQL) Restock(ctx context.Context, slotID int64, productID *int64, qty int) error {
	_, err := s.q.ExecContext(ctx, `UPDATE slots
		SET product_id = ?, next_product_id = NULL, qty = ?, needs_check = FALSE
		WHERE id = ?`, productID, qty, slotID)
	return err
}

func (s *MySQL) MachineIDsHolding(ctx context.Context, productID int64) ([]int64, error) {
	// No lock is taken here, deliberately. The caller goes on to lock those
	// machines, and a purchase locks machine before slot; holding slot locks
	// while waiting for a machine row is the one order that deadlocks.
	rows, err := s.q.QueryContext(ctx,
		`SELECT DISTINCT machine_id FROM slots WHERE product_id = ?`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *MySQL) CreateMany(ctx context.Context, machineID int64, count, capacity int) error {
	if count <= 0 {
		return nil
	}
	args := make([]any, 0, count*3)
	for n := 1; n <= count; n++ {
		args = append(args, machineID, n, capacity)
	}
	values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, 0, FALSE),", count), ",")
	_, err := s.q.ExecContext(ctx, `INSERT INTO slots
		(machine_id, slot_number, capacity, qty, needs_check) VALUES `+values, args...)
	return err
}

func (s *MySQL) Get(ctx context.Context, machineID int64, slotNumber int) (model.Slot, error) {
	sl, err := scanSlot(s.q.QueryRowContext(ctx, byNumberQuery, machineID, slotNumber))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Slot{}, model.ErrNoRecord
	}
	return sl, err
}

func (s *MySQL) SetPlan(ctx context.Context, slotID int64, capacity int, nextProductID *int64) error {
	_, err := s.q.ExecContext(ctx,
		`UPDATE slots SET capacity = ?, next_product_id = ? WHERE id = ?`, capacity, nextProductID, slotID)
	return err
}
