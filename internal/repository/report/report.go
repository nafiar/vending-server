// Package report answers the aggregate questions the admin screens ask. The
// port is here; the drivers live in subpackages.
//
// It is read-only by design: there is no write anywhere in it. These are
// questions about data other repositories own — how much a machine took this
// month, when it last sold anything, how many visits an operator has made —
// and answering them by summing in the database is the whole point. A usecase
// that fetched the rows and added them up in Go would move a year of
// transactions across the wire to produce one number.
//
// Every method returns a map keyed by the id asked about, and a key missing
// from the result means zero. That is what lets one query answer for every
// machine or operator at once instead of one query each.
package report

import (
	"context"
	"time"

	"vending/internal/model"
)

//go:generate mockgen -source=report.go -destination=mock/report_mock.go -package=mock

// Repository is the aggregate reads. machineID 0 means every machine, which is
// how the list screen gets its figures in one query rather than one per card.
type Repository interface {
	// SlotStats counts each machine's slots, the ones with nothing to sell, and
	// the ones a failed dispense has blocked.
	SlotStats(ctx context.Context, machineID int64) (map[int64]model.SlotStats, error)

	// MachineSales sums the revenue and the number of sales per machine since
	// an instant. Only DISPENSED transactions count (BR-03).
	MachineSales(ctx context.Context, since time.Time, machineID int64) (map[int64]model.Totals, error)

	// MachineLastSale is when each machine last dispensed something. A machine
	// that never has is absent from the result.
	MachineLastSale(ctx context.Context, machineID int64) (map[int64]time.Time, error)

	// OperatorVisits counts each user's restock visits since an instant.
	OperatorVisits(ctx context.Context, since time.Time) (map[int64]int64, error)

	// OperatorLastVisit is each user's most recent visit, with the machine it
	// was to. A user who has never restocked anything is absent.
	OperatorLastVisit(ctx context.Context) (map[int64]model.Visit, error)

	// Summary answers the four tiles on the admin home in one query (FR-17).
	Summary(ctx context.Context, w model.SummaryWindows, machineID int64) (model.Summary, error)

	// RecapTotals is the recap's headline figures over one span: what was
	// earned, and what needs looking at.
	RecapTotals(ctx context.Context, f model.ReportFilter) (model.RecapTotals, error)

	// RevenueByLocalDay sums revenue per local calendar day, keyed by the day in
	// YYYY-MM-DD. offsetSeconds is the zone's offset from UTC, which the query
	// shifts created_at by so that the grouping is by local day.
	//
	// It is always by day, never by week or month: folding days into larger
	// buckets is calendar arithmetic, and the caller does it, so that a week
	// starting on Monday stays one idea in one place.
	RevenueByLocalDay(ctx context.Context, f model.ReportFilter, offsetSeconds int) (map[string]model.Totals, error)

	// RevenueByMachine is each machine's revenue over the span, with the
	// machine's own details, so the recap table needs no second lookup. Every
	// machine appears, including the ones that sold nothing.
	RevenueByMachine(ctx context.Context, f model.ReportFilter) ([]model.MachineRevenue, error)

	// RevenueByProduct is each product's revenue over the span, best first.
	RevenueByProduct(ctx context.Context, f model.ReportFilter) ([]model.ProductRevenue, error)

	// RevenueBySlot is each slot's revenue over the span, best first.
	RevenueBySlot(ctx context.Context, f model.ReportFilter) ([]model.SlotRevenue, error)

	// Reconcile checks every slot's bookkeeping (NFR-05): the count the last
	// operator visit left, minus what was sold since, against the count held
	// now. Slots nobody has ever restocked come back with NeverRestocked set.
	//
	// It is here, and takes no filter, because it is the one question that
	// spans every table at once -- slots, visits and sales -- and it is asked
	// of the whole system or not at all.
	Reconcile(ctx context.Context) ([]model.SlotReconciliation, error)
}
