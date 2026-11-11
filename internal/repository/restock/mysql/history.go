package mysql

import (
	"context"
	"database/sql"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/restock"
)

// The restock history (UC-10, FR-25), read-only.

// Compile-time proof that the driver still matches the read port.
var _ restock.History = (*MySQL)(nil)

// itemChunk is how many visits' lines are fetched per query. The page is small,
// but an export asks for every visit in a span, and an IN list is not the place
// to find out how long a query may be.
const itemChunk = 1000

// List returns one page of visits with their lines.
//
// It is two queries and not a join: a join would repeat every visit once per
// slot, which for a 30-slot machine is thirty copies of the same row to send
// and then fold back together. So the page is read first, then the lines of
// exactly those visits.
func (s *MySQL) List(ctx context.Context, f model.RestockFilter) (model.RestockHistory, error) {
	where := ` WHERE rs.started_at >= ? AND rs.started_at < ?`
	args := []any{f.Start, f.End}
	if f.MachineID != 0 {
		where += ` AND rs.machine_id = ?`
		args = append(args, f.MachineID)
	}
	if f.UserID != 0 {
		where += ` AND rs.user_id = ?`
		args = append(args, f.UserID)
	}

	out := model.RestockHistory{Rows: []model.RestockVisit{}}
	if err := s.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM restock_sessions rs`+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}

	rows, err := s.q.QueryContext(ctx, `
		SELECT rs.id, rs.client_id, rs.started_at, rs.finished_at, rs.note,
		       m.id, m.code, m.name, u.id, u.name, u.username
		FROM restock_sessions rs
		JOIN machines m ON m.id = rs.machine_id
		JOIN users u ON u.id = rs.user_id`+where+`
		ORDER BY rs.started_at DESC, rs.id DESC
		LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return out, err
	}
	// index maps a visit id to its place on the page, so the lines below can be
	// hung on the right row without searching for it.
	index := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var v model.RestockVisit
		var started, finished sql.NullTime
		var note sql.NullString
		if err := rows.Scan(&v.ID, &v.ClientID, &started, &finished, &note,
			&v.MachineID, &v.MachineCode, &v.MachineName,
			&v.OperatorID, &v.OperatorName, &v.OperatorUsername); err != nil {
			rows.Close()
			return out, err
		}
		v.StartedAt, v.FinishedAt = started.Time, finished.Time
		v.Note = mysqldb.NullString(note)
		v.Items = []model.RestockVisitItem{}
		index[v.ID] = len(out.Rows)
		ids = append(ids, v.ID)
		out.Rows = append(out.Rows, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}

	for start := 0; start < len(ids); start += itemChunk {
		chunk := ids[start:min(start+itemChunk, len(ids))]
		if err := s.appendItems(ctx, out.Rows, index, chunk); err != nil {
			return out, err
		}
	}
	return out, nil
}

// appendItems reads the lines of one chunk of visits and hangs each on its own
// row. Lines come back ordered by slot number, which is the order the screen
// shows them in.
func (s *MySQL) appendItems(ctx context.Context, visits []model.RestockVisit, index map[int64]int, ids []int64) error {
	rows, err := s.q.QueryContext(ctx, `
		SELECT ri.session_id, s.slot_number, ri.product_id, p.name,
		       ri.qty_before, ri.qty_after, ri.product_changed
		FROM restock_items ri
		JOIN slots s ON s.id = ri.slot_id
		LEFT JOIN products p ON p.id = ri.product_id
		WHERE ri.session_id IN (`+mysqldb.Placeholders(len(ids))+`)
		ORDER BY ri.session_id, s.slot_number`, mysqldb.Args(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sessionID int64
		var it model.RestockVisitItem
		var productID sql.NullInt64
		var productName sql.NullString
		if err := rows.Scan(&sessionID, &it.SlotNumber, &productID, &productName,
			&it.QtyBefore, &it.QtyAfter, &it.ProductChanged); err != nil {
			return err
		}
		it.ProductID, it.ProductName = mysqldb.NullInt(productID), mysqldb.NullString(productName)
		v := &visits[index[sessionID]]
		v.Items = append(v.Items, it)
	}
	return rows.Err()
}
