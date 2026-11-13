// Package recap reports what the machines earned (UC-06, FR-17, FR-18).
//
// Everything here is read-only, and one rule governs every figure: revenue is
// the sum of price over DISPENSED transactions and nothing else (BR-03), at the
// price copied into each transaction when it started (BR-05). A price change
// today never rewrites what last month earned, and an attempt that failed, was
// cancelled or expired is counted as an attempt, never as money.
//
// The division of labour with the repository is deliberate. MySQL sums, because
// adding up a year of transactions in Go would mean moving a year of
// transactions. This package does the calendar arithmetic — which local day a
// sale falls in, which week or month that day belongs to, and which buckets
// exist at all — because a week that starts on Monday is one idea and it should
// live in one place (common/calendar).
//
// That is also why the chart is filled in here rather than queried: a day with
// no sales has no row to return, and a chart that skips it makes a quiet week
// look like a short one.
package recap

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"vending/internal/common/calendar"
	"vending/internal/common/clock"
	"vending/internal/common/paging"
	"vending/internal/model"
	"vending/internal/repository/report"
	"vending/internal/repository/transaction"
)

//go:generate mockgen -source=recap.go -destination=mock/recap_mock.go -package=mock

// The spans the screens may ask for. A recap defaults to the last 30 days, and
// two years is the ceiling: beyond that the answer is a report to export, not a
// page to draw.
const (
	defaultRecapDays = 30
	maxRangeDays     = 731
	// summaryDays is the "last 7 days" tile: today and the six before it.
	summaryDays = 7
	// defaultHistoryDays is the transaction history's span: the last week,
	// which is the question that screen is usually opened with.
	defaultHistoryDays = 7
	// historyPageSize is one screenful of attempts.
	historyPageSize = 25
	// topProducts is how many products the recap highlights.
	topProducts = 5
)

// Query is a request for a recap.
type Query struct {
	From, To  string
	MachineID int64
	// GroupBy is day, week or month; empty means day.
	GroupBy string
	// Breakdown is product or slot; empty means product.
	Breakdown string
}

// HistoryQuery is a request for the transaction history. From and To are local
// calendar days (YYYY-MM-DD); empty means the last week. A zero MachineID,
// SlotNumber, Status or Flag means "don't filter on it".
type HistoryQuery struct {
	From, To   string
	MachineID  int64
	SlotNumber int
	// Status is one of the six transaction states.
	Status string
	// Flag narrows to what needs review: problem or reported (FR-24).
	Flag           string
	Page, PageSize int
	// All asks for every matching attempt instead of one page, which is what
	// the CSV export does.
	All bool
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// Summary is the four tiles at the top of the admin home (FR-17).
	Summary(ctx context.Context, machineID int64) (model.Summary, error)
	// Recap is the revenue recap (UC-06, FR-17, FR-18).
	Recap(ctx context.Context, q Query) (model.Recap, error)
	// History is the transaction history: every purchase attempt, newest
	// first, with the figures for the whole filtered set (UC-09, FR-24).
	History(ctx context.Context, q HistoryQuery) (model.TransactionHistory, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	reports report.Repository
	history transaction.History
	clock   clock.Clock
	// loc is the zone whose calendar days every figure is grouped by.
	loc *time.Location
	log *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(reports report.Repository, history transaction.History, c clock.Clock, loc *time.Location, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{reports: reports, history: history, clock: c, loc: loc, log: log}
}

// Summary works out the four windows in the server's zone and asks for them.
//
// "The same time yesterday" is yesterday up to the time of day it is now. That
// is the only comparison with today that means anything before the day is over:
// comparing a morning with a whole day would show a collapse every morning.
func (s *Service) Summary(ctx context.Context, machineID int64) (model.Summary, error) {
	now := s.clock.Now().In(s.loc)
	today := calendar.StartOfDay(now)
	yesterday := today.AddDate(0, 0, -1)

	return s.reports.Summary(ctx, model.SummaryWindows{
		TodayStart:     today.UTC(),
		YesterdayStart: yesterday.UTC(),
		// The same elapsed time into yesterday as has passed today.
		YesterdayUntil: yesterday.Add(now.Sub(today)).UTC(),
		Last7Start:     today.AddDate(0, 0, -(summaryDays - 1)).UTC(),
		MonthStart:     calendar.StartOfMonth(now).UTC(),
	}, machineID)
}

// Recap builds the whole recap.
func (s *Service) Recap(ctx context.Context, q Query) (model.Recap, error) {
	groupBy, breakdown, err := q.clean()
	if err != nil {
		return model.Recap{}, err
	}
	r, err := calendar.ParseRange(s.clock.Now(), s.loc, q.From, q.To, defaultRecapDays, maxRangeDays)
	if err != nil {
		return model.Recap{}, err
	}
	filter := model.ReportFilter{Start: r.Start, End: r.End, MachineID: q.MachineID}

	out := model.Recap{From: r.From, To: r.To, GroupBy: groupBy}
	if out.Totals, err = s.reports.RecapTotals(ctx, filter); err != nil {
		return model.Recap{}, err
	}
	if out.Series, err = s.series(ctx, filter, r, groupBy); err != nil {
		return model.Recap{}, err
	}
	if out.ByMachine, err = s.reports.RevenueByMachine(ctx, filter); err != nil {
		return model.Recap{}, err
	}

	byProduct, err := s.reports.RevenueByProduct(ctx, filter)
	if err != nil {
		return model.Recap{}, err
	}
	out.TopProducts = productRows(byProduct)
	if len(out.TopProducts) > topProducts {
		out.TopProducts = out.TopProducts[:topProducts]
	}

	// The breakdown is by product unless slots were asked for; top products is
	// always by product either way.
	out.Breakdown = productRows(byProduct)
	if breakdown == "slot" {
		bySlot, err := s.reports.RevenueBySlot(ctx, filter)
		if err != nil {
			return model.Recap{}, err
		}
		out.Breakdown = slotRows(bySlot)
	}
	return out, nil
}

// clean fills in the defaults and refuses anything else. Both are reported at
// once, so a screen sending two bad parameters hears about both.
func (q Query) clean() (groupBy, breakdown string, err error) {
	groupBy, breakdown = q.GroupBy, q.Breakdown
	if groupBy == "" {
		groupBy = "day"
	}
	if breakdown == "" {
		breakdown = "product"
	}
	fe := model.FieldErrors{}
	switch groupBy {
	case "day", "week", "month":
	default:
		fe.Add("groupBy", "Kelompokkan per day, week atau month.")
	}
	switch breakdown {
	case "product", "slot":
	default:
		fe.Add("breakdown", "Rincian per product atau slot.")
	}
	return groupBy, breakdown, fe.Err()
}

// series folds the daily figures into the buckets asked for, and includes every
// bucket in the range even when it is empty.
func (s *Service) series(ctx context.Context, f model.ReportFilter, r calendar.DateRange, groupBy string) ([]model.SeriesPoint, error) {
	offset := calendar.UTCOffsetSeconds(s.loc, r.Start)
	byDay, err := s.reports.RevenueByLocalDay(ctx, f, offset)
	if err != nil {
		return nil, err
	}

	// Each local day is added to the bucket it belongs to, which for "day" is
	// itself and for "week" or "month" is the one it falls in.
	buckets := map[string]model.Totals{}
	for day, totals := range byDay {
		at, err := time.ParseInLocation(calendar.DayLayout, day, s.loc)
		if err != nil {
			// The repository formats these, so this cannot happen unless the
			// format changes on one side only; saying so beats a silent zero.
			return nil, err
		}
		key := calendar.PeriodKey(at, groupBy)
		b := buckets[key]
		b.Revenue += totals.Revenue
		b.Sales += totals.Sales
		buckets[key] = b
	}

	out := make([]model.SeriesPoint, 0, len(byDay))
	for _, p := range calendar.Periods(r, groupBy) {
		out = append(out, model.SeriesPoint{Period: p.Key, Start: p.Start, Totals: buckets[p.Key]})
	}
	return out, nil
}

// productRows turns the product figures into breakdown lines.
func productRows(list []model.ProductRevenue) []model.BreakdownRow {
	out := make([]model.BreakdownRow, 0, len(list))
	for _, p := range list {
		out = append(out, model.BreakdownRow{
			Key:     strconv.FormatInt(p.ProductID, 10),
			Label:   p.Name,
			Revenue: p.Totals.Revenue,
			Sales:   p.Totals.Sales,
		})
	}
	return out
}

// slotRows turns the slot figures into breakdown lines.
//
// The label names the coil and, in brackets, what is in it today. Today's
// product is not necessarily what the coil sold over the period — a visit may
// have changed it — so it is a hint about where to look, not part of the
// figure.
func slotRows(list []model.SlotRevenue) []model.BreakdownRow {
	out := make([]model.BreakdownRow, 0, len(list))
	for _, sl := range list {
		label := sl.MachineCode + " · Slot " + strconv.Itoa(sl.SlotNumber)
		if sl.CurrentProduct != nil {
			label += " (" + *sl.CurrentProduct + ")"
		}
		out = append(out, model.BreakdownRow{
			// The machine and the slot together, because slot numbers repeat
			// across machines and a row has to identify one coil.
			Key:     strconv.FormatInt(sl.MachineID, 10) + "-" + strconv.Itoa(sl.SlotNumber),
			Label:   label,
			Revenue: sl.Totals.Revenue,
			Sales:   sl.Totals.Sales,
		})
	}
	return out
}

// History is the transaction history (UC-09, FR-24): every purchase attempt in
// the span, newest first, one page at a time.
//
// It refuses a status or a review filter it does not know rather than ignoring
// it. A filter silently dropped is worse than an error here: the screen would
// show every attempt while saying it was showing the failed ones.
func (s *Service) History(ctx context.Context, q HistoryQuery) (model.TransactionHistory, error) {
	r, err := calendar.ParseRange(s.clock.Now(), s.loc, q.From, q.To, defaultHistoryDays, maxRangeDays)
	if err != nil {
		return model.TransactionHistory{}, err
	}
	fe := model.FieldErrors{}
	if q.Status != "" && !model.TransactionStatuses[q.Status] {
		fe.Add("status", "Status tidak dikenal.")
	}
	if q.Flag != "" && q.Flag != model.FlagProblem && q.Flag != model.FlagReported {
		fe.Add("flag", "Filter tinjauan harus problem atau reported.")
	}
	if err := fe.Err(); err != nil {
		return model.TransactionHistory{}, err
	}

	page := paging.Of(q.Page, q.PageSize, historyPageSize)
	limit, offset := page.Window(q.All)
	out, err := s.history.List(ctx, model.TransactionFilter{
		Start: r.Start, End: r.End,
		MachineID: q.MachineID, SlotNumber: q.SlotNumber,
		Status: q.Status, Flag: q.Flag,
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return model.TransactionHistory{}, err
	}
	// Which page this is was the caller's decision, so the usecase says it.
	out.Page, out.PageSize = page.Number, page.Size
	return out, nil
}
