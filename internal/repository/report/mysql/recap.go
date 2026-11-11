package mysql

import (
	"context"
	"database/sql"
	"time"

	"vending/internal/model"
)

// The recap queries. Each one sums in the database and returns figures, for the
// reason the port's package comment gives: the alternative is moving a year of
// transactions across the wire to produce one number.

// span is the WHERE clause every recap query shares, and its arguments.
//
// The transactions are filtered on created_at rather than finished_at, so a
// purchase belongs to the day it was started on. That is the day the customer
// was standing there, and the day the kiosk's own figures would put it in.
func span(f model.ReportFilter, alias string) (string, []any) {
	p := alias + "."
	where := ` WHERE ` + p + `created_at >= ? AND ` + p + `created_at < ?`
	args := []any{f.Start, f.End}
	if f.MachineID != 0 {
		where += ` AND ` + p + `machine_id = ?`
		args = append(args, f.MachineID)
	}
	return where, args
}

// sold narrows a span to the transactions that are revenue (BR-03).
func sold(f model.ReportFilter, alias string) (string, []any) {
	where, args := span(f, alias)
	return where + ` AND ` + alias + `.status = ?`, append(args, model.StatusDispensed)
}

func (s *MySQL) Summary(ctx context.Context, w model.SummaryWindows, machineID int64) (model.Summary, error) {
	// All four windows in one pass. The outer bound is the earliest of them, so
	// the scan reads only what at least one window needs.
	lower := w.YesterdayStart
	for _, t := range []time.Time{w.TodayStart, w.Last7Start, w.MonthStart} {
		if t.Before(lower) {
			lower = t
		}
	}
	q := `SELECT
		COALESCE(SUM(CASE WHEN created_at >= ? THEN price END), 0), COALESCE(SUM(created_at >= ?), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? THEN price END), 0), COALESCE(SUM(created_at >= ? AND created_at < ?), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? THEN price END), 0), COALESCE(SUM(created_at >= ?), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? THEN price END), 0), COALESCE(SUM(created_at >= ?), 0)
		FROM transactions WHERE status = ? AND created_at >= ?`
	args := []any{
		w.TodayStart, w.TodayStart,
		w.YesterdayStart, w.YesterdayUntil, w.YesterdayStart, w.YesterdayUntil,
		w.Last7Start, w.Last7Start,
		w.MonthStart, w.MonthStart,
		model.StatusDispensed, lower,
	}
	if machineID != 0 {
		q += ` AND machine_id = ?`
		args = append(args, machineID)
	}
	var out model.Summary
	err := s.q.QueryRowContext(ctx, q, args...).Scan(
		&out.Today.Revenue, &out.Today.Sales,
		&out.SameTimeYesterday.Revenue, &out.SameTimeYesterday.Sales,
		&out.Last7.Revenue, &out.Last7.Sales,
		&out.Month.Revenue, &out.Month.Sales)
	return out, err
}

func (s *MySQL) RecapTotals(ctx context.Context, f model.ReportFilter) (model.RecapTotals, error) {
	where, args := span(f, "t")
	var out model.RecapTotals
	err := s.q.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN t.status = ? THEN t.price END), 0),
			COALESCE(SUM(t.status = ?), 0),
			COALESCE(SUM(t.status = ?), 0),
			COALESCE(SUM(t.customer_reported_at IS NOT NULL), 0),
			COALESCE(SUM(t.status IN (?, ?)), 0)
		FROM transactions t`+where,
		append([]any{
			model.StatusDispensed, model.StatusDispensed, model.StatusFailed,
			model.StatusCancelled, model.StatusExpired,
		}, args...)...).
		Scan(&out.Revenue, &out.Sales, &out.Failed, &out.Reported, &out.Cancelled)
	return out, err
}

func (s *MySQL) RevenueByLocalDay(ctx context.Context, f model.ReportFilter, offsetSeconds int) (map[string]model.Totals, error) {
	where, args := sold(f, "t")
	// created_at is shifted by the zone's offset before the date is taken, so
	// the grouping is by local calendar day rather than by UTC day.
	rows, err := s.q.QueryContext(ctx, `
		SELECT DATE_FORMAT(DATE_ADD(t.created_at, INTERVAL ? SECOND), '%Y-%m-%d') AS d,
		       SUM(t.price), COUNT(*)
		FROM transactions t`+where+`
		GROUP BY d`, append([]any{offsetSeconds}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.Totals{}
	for rows.Next() {
		var day string
		var t model.Totals
		if err := rows.Scan(&day, &t.Revenue, &t.Sales); err != nil {
			return nil, err
		}
		out[day] = t
	}
	return out, rows.Err()
}

func (s *MySQL) RevenueByMachine(ctx context.Context, f model.ReportFilter) ([]model.MachineRevenue, error) {
	// A left join from machines, so a machine that sold nothing over the period
	// still has a row: "VM-02 took nothing this week" is the answer somebody is
	// looking for, and leaving it out hides it. That is why this one does not
	// use span(): the period has to be a join condition, not a WHERE, or the
	// join would drop the very rows it is there to keep.
	q := `SELECT m.id, m.code, m.name, m.location, m.is_active, m.last_seen_at,
	             COALESCE(SUM(t.price), 0), COUNT(t.id), MAX(t.created_at)
		FROM machines m
		LEFT JOIN transactions t ON t.machine_id = m.id
		 AND t.created_at >= ? AND t.created_at < ? AND t.status = ?`
	args := []any{f.Start, f.End, model.StatusDispensed}
	if f.MachineID != 0 {
		q += ` WHERE m.id = ?`
		args = append(args, f.MachineID)
	}
	// Best first, then by code so that ties are in a stable, readable order.
	q += ` GROUP BY m.id, m.code, m.name, m.location, m.is_active, m.last_seen_at
		ORDER BY COALESCE(SUM(t.price), 0) DESC, m.code`
	rows, err := s.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.MachineRevenue{}
	for rows.Next() {
		var m model.MachineRevenue
		var location sql.NullString
		var seen, lastSale sql.NullTime
		if err := rows.Scan(&m.MachineID, &m.Code, &m.Name, &location, &m.IsActive, &seen,
			&m.Totals.Revenue, &m.Totals.Sales, &lastSale); err != nil {
			return nil, err
		}
		m.Location = location.String
		m.LastSeenAt, m.LastSaleAt = nullTime(seen), nullTime(lastSale)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *MySQL) RevenueByProduct(ctx context.Context, f model.ReportFilter) ([]model.ProductRevenue, error) {
	where, args := sold(f, "t")
	rows, err := s.q.QueryContext(ctx, `
		SELECT t.product_id, p.name, SUM(t.price) AS revenue, COUNT(*) AS sales
		FROM transactions t JOIN products p ON p.id = t.product_id`+where+`
		GROUP BY t.product_id, p.name
		ORDER BY revenue DESC, sales DESC, p.name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProductRevenue{}
	for rows.Next() {
		var p model.ProductRevenue
		if err := rows.Scan(&p.ProductID, &p.Name, &p.Totals.Revenue, &p.Totals.Sales); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *MySQL) RevenueBySlot(ctx context.Context, f model.ReportFilter) ([]model.SlotRevenue, error) {
	where, args := sold(f, "t")
	// The product join is a left join and reads the slot's *current* product:
	// what the coil holds today, which may not be what it sold. A slot emptied
	// since then still has its figures.
	rows, err := s.q.QueryContext(ctx, `
		SELECT t.machine_id, m.code, s.slot_number, cp.name,
		       SUM(t.price) AS revenue, COUNT(*) AS sales
		FROM transactions t
		JOIN slots s ON s.id = t.slot_id
		JOIN machines m ON m.id = t.machine_id
		LEFT JOIN products cp ON cp.id = s.product_id`+where+`
		GROUP BY t.machine_id, m.code, s.slot_number, cp.name
		ORDER BY revenue DESC, m.code, s.slot_number`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SlotRevenue{}
	for rows.Next() {
		var sr model.SlotRevenue
		var current sql.NullString
		if err := rows.Scan(&sr.MachineID, &sr.MachineCode, &sr.SlotNumber, &current,
			&sr.Totals.Revenue, &sr.Totals.Sales); err != nil {
			return nil, err
		}
		if current.Valid {
			name := current.String
			sr.CurrentProduct = &name
		}
		out = append(out, sr)
	}
	return out, rows.Err()
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
