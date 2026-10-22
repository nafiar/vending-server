package core

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"time"
)

// Revenue is always the sum of price over DISPENSED transactions (BR-03): the
// price copied into each transaction when it was created (BR-05), so a later
// price change never rewrites history.

// GetSummary returns today, the same time yesterday, the last 7 days and this
// month, for the tiles at the top of the admin home (FR-17).
func (s *Service) GetSummary(ctx context.Context, machineID int64) (Summary, error) {
	nowL := s.now().In(s.loc)
	today := startOfDay(nowL)
	yStart := today.AddDate(0, 0, -1)
	yNow := yStart.Add(nowL.Sub(today))
	last7 := today.AddDate(0, 0, -6)
	month := startOfMonth(nowL)

	lower := yStart
	for _, t := range []time.Time{last7, month} {
		if t.Before(lower) {
			lower = t
		}
	}
	q := `SELECT
		COALESCE(SUM(CASE WHEN created_at >= ? THEN price END), 0), COALESCE(SUM(created_at >= ?), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? AND created_at < ? THEN price END), 0), COALESCE(SUM(created_at >= ? AND created_at < ?), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? THEN price END), 0), COALESCE(SUM(created_at >= ?), 0),
		COALESCE(SUM(CASE WHEN created_at >= ? THEN price END), 0), COALESCE(SUM(created_at >= ?), 0)
		FROM transactions WHERE status = 'DISPENSED' AND created_at >= ?`
	t0, y0, y1, l7, m0 := today.UTC(), yStart.UTC(), yNow.UTC(), last7.UTC(), month.UTC()
	args := []any{t0, t0, y0, y1, y0, y1, l7, l7, m0, m0, lower.UTC()}
	if machineID != 0 {
		q += ` AND machine_id = ?`
		args = append(args, machineID)
	}
	var out Summary
	err := s.db.QueryRowContext(ctx, q, args...).Scan(
		&out.Today.Revenue, &out.Today.Sales,
		&out.SameTimeYesterday.Revenue, &out.SameTimeYesterday.Sales,
		&out.Last7.Revenue, &out.Last7.Sales,
		&out.Month.Revenue, &out.Month.Sales)
	return out, err
}

// RecapQuery is the query of GET /admin/recap.
type RecapQuery struct {
	From, To  string
	MachineID int64
	GroupBy   string // day (default), week or month
	Breakdown string // product (default) or slot
}

// GetRecap is the revenue recap (UC-06, FR-17, FR-18).
func (s *Service) GetRecap(ctx context.Context, q RecapQuery) (Recap, error) {
	fe := fieldErrors{}
	if q.GroupBy == "" {
		q.GroupBy = "day"
	}
	if q.Breakdown == "" {
		q.Breakdown = "product"
	}
	if q.GroupBy != "day" && q.GroupBy != "week" && q.GroupBy != "month" {
		fe.add("groupBy", "Kelompokkan per day, week atau month.")
	}
	if q.Breakdown != "product" && q.Breakdown != "slot" {
		fe.add("breakdown", "Rincian per product atau slot.")
	}
	if err := fe.err(); err != nil {
		return Recap{}, err
	}
	r, err := s.parseRange(q.From, q.To, 30, 731)
	if err != nil {
		return Recap{}, err
	}

	where := ` WHERE t.created_at >= ? AND t.created_at < ?`
	args := []any{r.Start, r.End}
	if q.MachineID != 0 {
		where += ` AND t.machine_id = ?`
		args = append(args, q.MachineID)
	}
	sold := where + ` AND t.status = 'DISPENSED'`

	out := Recap{From: r.From, To: r.To, GroupBy: q.GroupBy}

	// Totals: revenue and sales, plus the attempts that need attention.
	err = s.db.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(CASE WHEN t.status = 'DISPENSED' THEN t.price END), 0),
			COALESCE(SUM(t.status = 'DISPENSED'), 0),
			COALESCE(SUM(t.status = 'FAILED'), 0),
			COALESCE(SUM(t.customer_reported_at IS NOT NULL), 0),
			COALESCE(SUM(t.status IN ('CANCELLED', 'EXPIRED')), 0)
		FROM transactions t`+where, args...).
		Scan(&out.Totals.Revenue, &out.Totals.Sales, &out.Totals.Failed, &out.Totals.Reported, &out.Totals.Cancelled)
	if err != nil {
		return Recap{}, err
	}

	// Series: MySQL sums per local calendar day; Go folds days into weeks or months
	// and fills the periods without sales with zero.
	offset := utcOffsetSeconds(s.loc, r.Start)
	rows, err := s.db.QueryContext(ctx, `
		SELECT DATE_FORMAT(DATE_ADD(t.created_at, INTERVAL ? SECOND), '%Y-%m-%d') AS d, SUM(t.price), COUNT(*)
		FROM transactions t`+sold+` GROUP BY d`, append([]any{offset}, args...)...)
	if err != nil {
		return Recap{}, err
	}
	buckets := map[string]*Totals{}
	for rows.Next() {
		var d string
		var rev, n int64
		if err := rows.Scan(&d, &rev, &n); err != nil {
			rows.Close()
			return Recap{}, err
		}
		day, err := time.ParseInLocation(dayLayout, d, s.loc)
		if err != nil {
			rows.Close()
			return Recap{}, err
		}
		k := periodKey(day, q.GroupBy)
		if buckets[k] == nil {
			buckets[k] = &Totals{}
		}
		buckets[k].Revenue += rev
		buckets[k].Sales += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Recap{}, err
	}
	for _, p := range periods(r, q.GroupBy) {
		pt := SeriesPoint{Period: p.key, Start: ms(p.start)}
		if b := buckets[p.key]; b != nil {
			pt.Revenue, pt.Sales = b.Revenue, b.Sales
		}
		out.Series = append(out.Series, pt)
	}

	if out.ByMachine, err = s.recapByMachine(ctx, sold, args, q.MachineID); err != nil {
		return Recap{}, err
	}

	byProduct, err := s.breakdownRows(ctx, `
		SELECT CAST(t.product_id AS CHAR), p.name, SUM(t.price) AS revenue, COUNT(*) AS sales
		FROM transactions t JOIN products p ON p.id = t.product_id`+sold+`
		GROUP BY t.product_id, p.name
		ORDER BY revenue DESC, sales DESC, p.name`, args)
	if err != nil {
		return Recap{}, err
	}
	out.TopProducts = byProduct
	if len(out.TopProducts) > 5 {
		out.TopProducts = out.TopProducts[:5]
	}
	out.Breakdown = byProduct
	if q.Breakdown == "slot" {
		if out.Breakdown, err = s.slotBreakdown(ctx, sold, args); err != nil {
			return Recap{}, err
		}
	}
	return out, nil
}

func (s *Service) recapByMachine(ctx context.Context, sold string, args []any, machineID int64) ([]MachineRecap, error) {
	mq := `SELECT id, code, name, location, is_active, last_seen_at FROM machines`
	var margs []any
	if machineID != 0 {
		mq += ` WHERE id = ?`
		margs = append(margs, machineID)
	}
	rows, err := s.db.QueryContext(ctx, mq+` ORDER BY code`, margs...)
	if err != nil {
		return nil, err
	}
	var out []MachineRecap
	index := map[int64]int{}
	for rows.Next() {
		var m MachineRecap
		var loc sql.NullString
		var seen sql.NullTime
		if err := rows.Scan(&m.MachineID, &m.Code, &m.Name, &loc, &m.IsActive, &seen); err != nil {
			rows.Close()
			return nil, err
		}
		m.Location = loc.String
		m.LastSeenAt = nullMs(seen)
		index[m.MachineID] = len(out)
		out = append(out, m)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT t.machine_id, SUM(t.price), COUNT(*), MAX(t.created_at)
		FROM transactions t`+sold+` GROUP BY t.machine_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, rev, n int64
		var last sql.NullTime
		if err := rows.Scan(&id, &rev, &n, &last); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Revenue, out[i].Sales, out[i].LastSaleAt = rev, n, nullMs(last)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revenue > out[j].Revenue })
	if out == nil {
		out = []MachineRecap{}
	}
	return out, nil
}

func (s *Service) breakdownRows(ctx context.Context, query string, args []any) ([]BreakdownRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BreakdownRow{}
	for rows.Next() {
		var b BreakdownRow
		if err := rows.Scan(&b.Key, &b.Label, &b.Revenue, &b.Sales); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) slotBreakdown(ctx context.Context, sold string, args []any) ([]BreakdownRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.machine_id, m.code, s.slot_number, cp.name, SUM(t.price) AS revenue, COUNT(*) AS sales
		FROM transactions t
		JOIN slots s ON s.id = t.slot_id
		JOIN machines m ON m.id = t.machine_id
		LEFT JOIN products cp ON cp.id = s.product_id`+sold+`
		GROUP BY t.machine_id, m.code, s.slot_number, cp.name
		ORDER BY revenue DESC, m.code, s.slot_number`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BreakdownRow{}
	for rows.Next() {
		var mid int64
		var code string
		var n int
		var current sql.NullString
		var b BreakdownRow
		if err := rows.Scan(&mid, &code, &n, &current, &b.Revenue, &b.Sales); err != nil {
			return nil, err
		}
		b.Key = strconv.FormatInt(mid, 10) + "-" + strconv.Itoa(n)
		b.Label = code + " · Slot " + strconv.Itoa(n)
		if current.Valid {
			b.Label += " (" + current.String + ")"
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
