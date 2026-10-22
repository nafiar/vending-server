package core

import (
	"context"
	"database/sql"
)

// SlotReconciliation compares a slot's count with its history (NFR-05):
// the count set at the last visit minus the DISPENSED sales since then
// should equal the count the system holds now.
type SlotReconciliation struct {
	MachineCode    string `json:"machineCode"`
	SlotNumber     int    `json:"slotNumber"`
	LastVisitID    int64  `json:"lastVisitId"`
	LastVisitAt    int64  `json:"lastVisitAt"`
	CountAtVisit   int    `json:"countAtVisit"`
	SoldSince      int    `json:"soldSince"`
	Expected       int    `json:"expected"`
	Actual         int    `json:"actual"`
	Matches        bool   `json:"matches"`
	NeverRestocked bool   `json:"neverRestocked"`
}

// Reconcile checks every slot. Slots that were never restocked are listed with
// NeverRestocked set and are expected to be at 0.
func (s *Service) Reconcile(ctx context.Context) ([]SlotReconciliation, error) {
	rows, err := s.db.QueryContext(ctx, `
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
	var out []SlotReconciliation
	for rows.Next() {
		var r SlotReconciliation
		var visitID sql.NullInt64
		var visitAt sql.NullTime
		var after sql.NullInt64
		var sold sql.NullInt64
		if err := rows.Scan(&r.MachineCode, &r.SlotNumber, &r.Actual, &visitID, &visitAt, &after, &sold); err != nil {
			return nil, err
		}
		if !visitID.Valid {
			r.NeverRestocked = true
			r.Matches = r.Actual == 0
			out = append(out, r)
			continue
		}
		r.LastVisitID, r.LastVisitAt = visitID.Int64, ms(visitAt.Time)
		r.CountAtVisit, r.SoldSince = int(after.Int64), int(sold.Int64)
		r.Expected = r.CountAtVisit - r.SoldSince
		if r.Expected < 0 {
			r.Expected = 0 // the count never goes below zero
		}
		r.Matches = r.Expected == r.Actual
		out = append(out, r)
	}
	return out, rows.Err()
}
