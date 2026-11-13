package mysql

import (
	"context"
	"database/sql"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
)

// The bookkeeping check (NFR-05).

// Reconcile compares every slot's count with its own history.
//
// The arithmetic is in the query because the question is "for each slot, what
// happened since its last visit", and the last visit is a different row per
// slot. Fetching every visit and every sale to work that out in Go would mean
// moving the whole history to count it.
//
// The subquery picks the newest restock_items row per slot by session id rather
// than by time: the session id is monotonic and a visit's own finished_at comes
// from the kiosk, which may have filed it late from a journal.
func (s *MySQL) Reconcile(ctx context.Context) ([]model.SlotReconciliation, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT m.code, s.slot_number, s.qty, rs.id, rs.finished_at, ri.qty_after,
		       (SELECT COUNT(*) FROM transactions t
		        WHERE t.slot_id = s.id AND t.status = 'DISPENSED' AND t.finished_at >= rs.finished_at) AS sold
		FROM slots s
		JOIN machines m ON m.id = s.machine_id
		LEFT JOIN restock_items ri ON ri.id = (
			SELECT ri2.id FROM restock_items ri2 WHERE ri2.slot_id = s.id ORDER BY ri2.session_id DESC LIMIT 1)
		LEFT JOIN restock_sessions rs ON rs.id = ri.session_id
		ORDER BY m.code, s.slot_number`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.SlotReconciliation
	for rows.Next() {
		var r model.SlotReconciliation
		var visitID, countAtVisit, sold sql.NullInt64
		var visitAt sql.NullTime
		if err := rows.Scan(&r.MachineCode, &r.SlotNumber, &r.Actual,
			&visitID, &visitAt, &countAtVisit, &sold); err != nil {
			return nil, err
		}
		if !visitID.Valid {
			// Nobody has ever restocked this slot, so the only count that can
			// be right is nothing in it.
			r.NeverRestocked = true
			r.Matches = r.Actual == 0
			out = append(out, r)
			continue
		}
		r.LastVisitID, r.LastVisitAt = visitID.Int64, mysqldb.Ms(visitAt.Time)
		r.CountAtVisit, r.SoldSince = int(countAtVisit.Int64), int(sold.Int64)
		r.Expected = max(r.CountAtVisit-r.SoldSince, 0) // a count never goes below zero
		r.Matches = r.Expected == r.Actual
		out = append(out, r)
	}
	return out, rows.Err()
}
