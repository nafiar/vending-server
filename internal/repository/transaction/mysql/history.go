package mysql

import (
	"context"
	"database/sql"

	"vending/internal/model"
	"vending/internal/repository/transaction"
)

// The transaction history (UC-09, FR-24), read-only.

// Compile-time proof that the driver still matches the read port. The same
// struct serves both: the history takes no lock and is always bound to the
// pool, while the state-machine steps run inside a unit of work.
var _ transaction.History = (*MySQL)(nil)

// List returns one page of attempts, newest first, with the figures for the
// whole filtered set.
//
// The figures come from their own COUNT/SUM over the same WHERE rather than
// from the page: a page of 25 cannot tell you what 4,812 attempts were worth.
// Both queries are built from one filter so the two can never drift apart.
func (s *MySQL) List(ctx context.Context, f model.TransactionFilter) (model.TransactionHistory, error) {
	where, args := historyWhere(f)

	out := model.TransactionHistory{Rows: []model.TransactionHistoryRow{}}
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN t.status = 'DISPENSED' THEN t.price END), 0),
			COALESCE(SUM(t.status = 'DISPENSED'), 0)
		FROM transactions t JOIN slots s ON s.id = t.slot_id`+where, args...).
		Scan(&out.Total, &out.DispensedRevenue, &out.DispensedCount)
	if err != nil {
		return out, err
	}

	rows, err := s.q.QueryContext(ctx, `
		SELECT t.id, t.created_at, t.paid_at, t.finished_at, m.id, m.code, m.name, s.slot_number,
		       p.id, p.name, t.price, t.status, t.failure_reason, t.customer_reported_at
		FROM transactions t
		JOIN slots s ON s.id = t.slot_id
		JOIN machines m ON m.id = t.machine_id
		JOIN products p ON p.id = t.product_id`+where+`
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var r model.TransactionHistoryRow
		var created, paid, finished, reported sql.NullTime
		var reason sql.NullString
		if err := rows.Scan(&r.ID, &created, &paid, &finished,
			&r.MachineID, &r.MachineCode, &r.MachineName, &r.SlotNumber,
			&r.ProductID, &r.ProductName, &r.Price, &r.Status, &reason, &reported); err != nil {
			return out, err
		}
		r.CreatedAt = created.Time
		r.PaidAt, r.FinishedAt, r.CustomerReportedAt = nullTime(paid), nullTime(finished), nullTime(reported)
		if reason.Valid {
			r.FailureReason = &reason.String
		}
		out.Rows = append(out.Rows, r)
	}
	return out, rows.Err()
}

// historyWhere builds the one WHERE both queries share.
//
// The two review filters are not statuses and cannot be expressed as one:
// "problem" is a failed dispense or a customer complaint, which are a status
// and a timestamp on different rows of the same table (BR-07, FR-09).
func historyWhere(f model.TransactionFilter) (string, []any) {
	where := ` WHERE t.created_at >= ? AND t.created_at < ?`
	args := []any{f.Start, f.End}
	if f.MachineID != 0 {
		where += ` AND t.machine_id = ?`
		args = append(args, f.MachineID)
	}
	if f.SlotNumber != 0 {
		where += ` AND s.slot_number = ?`
		args = append(args, f.SlotNumber)
	}
	if f.Status != "" {
		where += ` AND t.status = ?`
		args = append(args, f.Status)
	}
	switch f.Flag {
	case model.FlagProblem:
		where += ` AND (t.status = 'FAILED' OR t.customer_reported_at IS NOT NULL)`
	case model.FlagReported:
		where += ` AND t.customer_reported_at IS NOT NULL`
	}
	return where, args
}
