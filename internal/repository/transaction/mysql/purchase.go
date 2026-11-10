package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
)

// rowQuery reads a transaction with the slot it sold from and the product as it
// was sold. Both joins are inner: a transaction cannot exist without them.
const rowQuery = `
	SELECT t.id, t.machine_id, t.slot_id, s.slot_number, t.status, t.price, t.failure_reason,
	       t.created_at, t.paid_at, t.finished_at, t.customer_reported_at,
	       p.id, p.name, p.description, p.image_url
	FROM transactions t
	JOIN slots s ON s.id = t.slot_id
	JOIN products p ON p.id = t.product_id
	WHERE t.id = ?`

func scanRow(r mysqldb.RowScanner) (model.Transaction, error) {
	var t model.Transaction
	var reason, desc, img sql.NullString
	var paid, finished, reported sql.NullTime
	err := r.Scan(&t.ID, &t.MachineID, &t.SlotID, &t.SlotNumber, &t.Status, &t.Price, &reason,
		&t.CreatedAt, &paid, &finished, &reported,
		&t.Product.ID, &t.Product.Name, &desc, &img)
	t.FailureReason = mysqldb.NullString(reason)
	t.PaidAt, t.FinishedAt, t.CustomerReportedAt = nullTime(paid), nullTime(finished), nullTime(reported)
	t.Product.Description = desc.String
	t.Product.ImageURL = mysqldb.NullString(img)
	// The price the purchase copied is the price of this sale, not whatever the
	// product costs today (BR-05).
	t.Product.Price = t.Price
	return t, err
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

func (s *MySQL) Get(ctx context.Context, id string) (model.Transaction, error) {
	t, err := scanRow(s.q.QueryRowContext(ctx, rowQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Transaction{}, model.ErrNoRecord
	}
	return t, err
}

func (s *MySQL) LockForUpdate(ctx context.Context, id string) (model.Transaction, error) {
	// The lock is taken on the plain transactions row first: MySQL locks every
	// row a FOR UPDATE query joined, and locking the slot and the product here
	// would both break the system-wide lock order and block unrelated sales.
	var locked string
	err := s.q.QueryRowContext(ctx, `SELECT id FROM transactions WHERE id = ? FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Transaction{}, model.ErrNoRecord
	}
	if err != nil {
		return model.Transaction{}, err
	}
	return s.Get(ctx, id)
}

func (s *MySQL) CountOpenShared(ctx context.Context, slotID int64) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions
		WHERE slot_id = ? AND status IN (?, ?) LOCK IN SHARE MODE`,
		slotID, model.StatusPending, model.StatusPaid).Scan(&n)
	return n, err
}

func (s *MySQL) ExpireStale(ctx context.Context, slotID int64, startedBefore time.Time, window time.Duration) error {
	_, err := s.q.ExecContext(ctx, `UPDATE transactions
		SET status = ?, finished_at = DATE_ADD(created_at, INTERVAL ? SECOND)
		WHERE slot_id = ? AND status = ? AND created_at < ?`,
		model.StatusExpired, int(window/time.Second), slotID, model.StatusPending, startedBefore)
	return err
}

func (s *MySQL) Create(ctx context.Context, t model.Transaction) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO transactions
		(id, machine_id, slot_id, product_id, price, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.MachineID, t.SlotID, t.Product.ID, t.Price, model.StatusPending, t.CreatedAt)
	if mysqldb.IsDuplicateKey(err) {
		return model.ErrDuplicateID
	}
	return err
}

func (s *MySQL) MarkPaid(ctx context.Context, id string, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE transactions SET status = ?, paid_at = ? WHERE id = ?`,
		model.StatusPaid, at, id)
	return err
}

func (s *MySQL) Finish(ctx context.Context, id, status string, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE transactions SET status = ?, finished_at = ? WHERE id = ?`,
		status, at, id)
	return err
}

func (s *MySQL) Fail(ctx context.Context, id, reason string, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE transactions SET status = ?, failure_reason = ?, finished_at = ? WHERE id = ?`,
		model.StatusFailed, reason, at, id)
	return err
}

func (s *MySQL) MarkReported(ctx context.Context, id string, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE transactions SET customer_reported_at = ? WHERE id = ?`, at, id)
	return err
}
