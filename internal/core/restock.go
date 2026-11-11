package core

import (
	"context"
	"database/sql"
)

// ---------------------------------------------------------------- admin: restock history (UC-10)

// RestockQuery filters GET /admin/restock-sessions.
type RestockQuery struct {
	From, To  string
	MachineID int64
	UserID    int64
	Page      int
	PageSize  int
	All       bool
}

// ListRestockSessions returns one row per operator visit, newest first, each
// with its lines per slot (FR-25).
func (s *Service) ListRestockSessions(ctx context.Context, q RestockQuery) (RestockPage, error) {
	r, err := s.parseRange(q.From, q.To, 7, 731)
	if err != nil {
		return RestockPage{}, err
	}
	page, size := pageOf(q.Page, q.PageSize, 20)

	where := ` WHERE rs.started_at >= ? AND rs.started_at < ?`
	args := []any{r.Start, r.End}
	if q.MachineID != 0 {
		where += ` AND rs.machine_id = ?`
		args = append(args, q.MachineID)
	}
	if q.UserID != 0 {
		where += ` AND rs.user_id = ?`
		args = append(args, q.UserID)
	}
	out := RestockPage{Page: page, PageSize: size, Rows: []RestockSessionView{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM restock_sessions rs`+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	limit, offset := size, (page-1)*size
	if q.All {
		limit, offset = maxExportRows, 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.id, rs.client_id, rs.started_at, rs.finished_at, rs.note,
		       m.id, m.code, m.name, u.id, u.name, u.username
		FROM restock_sessions rs
		JOIN machines m ON m.id = rs.machine_id
		JOIN users u ON u.id = rs.user_id`+where+`
		ORDER BY rs.started_at DESC, rs.id DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return out, err
	}
	index := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var v RestockSessionView
		var started, finished sql.NullTime
		var note sql.NullString
		if err := rows.Scan(&v.ID, &v.ClientID, &started, &finished, &note, &v.Machine.ID, &v.Machine.Code, &v.Machine.Name,
			&v.Operator.ID, &v.Operator.Name, &v.Operator.Username); err != nil {
			rows.Close()
			return out, err
		}
		v.StartedAt, v.FinishedAt, v.Note = ms(started.Time), ms(finished.Time), nullString(note)
		v.Items = []RestockItemView{}
		index[v.ID] = len(out.Rows)
		ids = append(ids, v.ID)
		out.Rows = append(out.Rows, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}

	// The lines of every session on this page, in one query.
	for start := 0; start < len(ids); start += 1000 {
		chunk := ids[start:min(start+1000, len(ids))]
		irows, err := s.db.QueryContext(ctx, `
			SELECT ri.session_id, s.slot_number, ri.product_id, p.name, ri.qty_before, ri.qty_after, ri.product_changed
			FROM restock_items ri
			JOIN slots s ON s.id = ri.slot_id
			LEFT JOIN products p ON p.id = ri.product_id
			WHERE ri.session_id IN (`+placeholders(len(chunk))+`)
			ORDER BY ri.session_id, s.slot_number`, int64sToArgs(chunk)...)
		if err != nil {
			return out, err
		}
		for irows.Next() {
			var sid int64
			var it RestockItemView
			var pid sql.NullInt64
			var pname sql.NullString
			if err := irows.Scan(&sid, &it.SlotNumber, &pid, &pname, &it.QtyBefore, &it.QtyAfter, &it.ProductChanged); err != nil {
				irows.Close()
				return out, err
			}
			it.ProductID, it.ProductName = nullInt(pid), nullString(pname)
			v := &out.Rows[index[sid]]
			v.Items = append(v.Items, it)
			if it.ProductChanged {
				v.ItemsAdded += it.QtyAfter // the coil was emptied and refilled with the new product
			} else if d := it.QtyAfter - it.QtyBefore; d > 0 {
				v.ItemsAdded += d
			}
			if it.ProductChanged || it.QtyAfter != it.QtyBefore {
				v.SlotsChanged++
			}
		}
		irows.Close()
		if err := irows.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}
