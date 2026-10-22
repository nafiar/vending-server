package core

import (
	"context"
	"database/sql"
)

// TransactionQuery filters GET /admin/transactions.
type TransactionQuery struct {
	From, To   string
	MachineID  int64
	SlotNumber int
	Status     string // one of the six states, or empty
	Flag       string // problem: FAILED or customer-reported; reported: customer-reported only
	Page       int
	PageSize   int
	All        bool // every matching row, for the CSV export
}

const maxExportRows = 100_000

var transactionStatuses = map[string]bool{
	"PENDING": true, "PAID": true, "DISPENSED": true, "FAILED": true, "CANCELLED": true, "EXPIRED": true,
}

// ListTransactions is the transaction history (UC-09, FR-24): every purchase
// attempt, newest first, with the DISPENSED total of the whole filtered set.
func (s *Service) ListTransactions(ctx context.Context, q TransactionQuery) (TransactionPage, error) {
	r, err := s.parseRange(q.From, q.To, 7, 731)
	if err != nil {
		return TransactionPage{}, err
	}
	fe := fieldErrors{}
	if q.Status != "" && !transactionStatuses[q.Status] {
		fe.add("status", "Status tidak dikenal.")
	}
	if q.Flag != "" && q.Flag != "problem" && q.Flag != "reported" {
		fe.add("flag", "Filter tinjauan harus problem atau reported.")
	}
	if err := fe.err(); err != nil {
		return TransactionPage{}, err
	}
	page, size := pageOf(q.Page, q.PageSize, 25)

	where := ` WHERE t.created_at >= ? AND t.created_at < ?`
	args := []any{r.Start, r.End}
	if q.MachineID != 0 {
		where += ` AND t.machine_id = ?`
		args = append(args, q.MachineID)
	}
	if q.SlotNumber != 0 {
		where += ` AND s.slot_number = ?`
		args = append(args, q.SlotNumber)
	}
	if q.Status != "" {
		where += ` AND t.status = ?`
		args = append(args, q.Status)
	}
	switch q.Flag {
	case "problem":
		where += ` AND (t.status = 'FAILED' OR t.customer_reported_at IS NOT NULL)`
	case "reported":
		where += ` AND t.customer_reported_at IS NOT NULL`
	}

	out := TransactionPage{Page: page, PageSize: size, Rows: []TransactionRow{}}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN t.status = 'DISPENSED' THEN t.price END), 0),
			COALESCE(SUM(t.status = 'DISPENSED'), 0)
		FROM transactions t JOIN slots s ON s.id = t.slot_id`+where, args...).
		Scan(&out.Total, &out.DispensedRevenue, &out.DispensedCount)
	if err != nil {
		return out, err
	}

	limit, offset := size, (page-1)*size
	if q.All {
		limit, offset = maxExportRows, 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.created_at, t.paid_at, t.finished_at, m.id, m.code, m.name, s.slot_number,
		       p.id, p.name, t.price, t.status, t.failure_reason, t.customer_reported_at
		FROM transactions t
		JOIN slots s ON s.id = t.slot_id
		JOIN machines m ON m.id = t.machine_id
		JOIN products p ON p.id = t.product_id`+where+`
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var t TransactionRow
		var created, paid, finished, reported sql.NullTime
		var reason sql.NullString
		if err := rows.Scan(&t.ID, &created, &paid, &finished, &t.Machine.ID, &t.Machine.Code, &t.Machine.Name, &t.SlotNumber,
			&t.Product.ID, &t.Product.Name, &t.Price, &t.Status, &reason, &reported); err != nil {
			return out, err
		}
		t.CreatedAt = ms(created.Time)
		t.PaidAt, t.FinishedAt, t.CustomerReportedAt = nullMs(paid), nullMs(finished), nullMs(reported)
		t.FailureReason = nullString(reason)
		out.Rows = append(out.Rows, t)
	}
	return out, rows.Err()
}

func pageOf(page, size, def int) (int, int) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = def
	}
	if size > 200 {
		size = 200
	}
	return page, size
}
