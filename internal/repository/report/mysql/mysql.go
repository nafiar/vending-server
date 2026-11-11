// Package mysql answers the aggregate reads by summing in the database.
package mysql

import (
	"context"
	"database/sql"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/report"
)

// MySQL runs the aggregate queries.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ report.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool or one transaction.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

// filter narrows an aggregate to one machine, or to all of them for 0. The
// clause is written to append to a WHERE that already has a condition.
func filter(machineID int64) (string, []any) {
	if machineID == 0 {
		return "", nil
	}
	return ` AND machine_id = ?`, []any{machineID}
}

func (s *MySQL) SlotStats(ctx context.Context, machineID int64) (map[int64]model.SlotStats, error) {
	where, args := filter(machineID)
	rows, err := s.q.QueryContext(ctx, `
		SELECT machine_id, COUNT(*), SUM(qty = 0 OR product_id IS NULL), SUM(needs_check)
		FROM slots WHERE 1 = 1`+where+`
		GROUP BY machine_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]model.SlotStats{}
	for rows.Next() {
		var id int64
		var st model.SlotStats
		if err := rows.Scan(&id, &st.Count, &st.Empty, &st.Blocked); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

func (s *MySQL) MachineSales(ctx context.Context, since time.Time, machineID int64) (map[int64]model.Totals, error) {
	where, args := filter(machineID)
	rows, err := s.q.QueryContext(ctx, `
		SELECT machine_id, SUM(price), COUNT(*) FROM transactions
		WHERE status = ? AND created_at >= ?`+where+`
		GROUP BY machine_id`, append([]any{model.StatusDispensed, since}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]model.Totals{}
	for rows.Next() {
		var id int64
		var t model.Totals
		if err := rows.Scan(&id, &t.Revenue, &t.Sales); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

func (s *MySQL) MachineLastSale(ctx context.Context, machineID int64) (map[int64]time.Time, error) {
	where, args := filter(machineID)
	rows, err := s.q.QueryContext(ctx, `
		SELECT machine_id, MAX(created_at) FROM transactions
		WHERE status = ?`+where+`
		GROUP BY machine_id`, append([]any{model.StatusDispensed}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var at sql.NullTime
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		if at.Valid {
			out[id] = at.Time
		}
	}
	return out, rows.Err()
}

func (s *MySQL) OperatorVisits(ctx context.Context, since time.Time) (map[int64]int64, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT user_id, COUNT(*) FROM restock_sessions WHERE started_at >= ? GROUP BY user_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *MySQL) OperatorLastVisit(ctx context.Context) (map[int64]model.Visit, error) {
	// The join picks each user's highest session id, which is their latest:
	// MAX(started_at) would not say which machine that visit was to.
	rows, err := s.q.QueryContext(ctx, `
		SELECT rs.user_id, rs.started_at, m.code
		FROM restock_sessions rs
		JOIN (SELECT user_id, MAX(id) AS last_id FROM restock_sessions GROUP BY user_id) x
		  ON x.last_id = rs.id
		JOIN machines m ON m.id = rs.machine_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]model.Visit{}
	for rows.Next() {
		var id int64
		var at sql.NullTime
		var code string
		if err := rows.Scan(&id, &at, &code); err != nil {
			return nil, err
		}
		out[id] = model.Visit{At: at.Time, MachineCode: code}
	}
	return out, rows.Err()
}
