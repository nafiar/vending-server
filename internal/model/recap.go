package model

import "time"

// The figures the admin screens report, and the windows they are measured over.
//
// One rule governs every number in this file: revenue is the sum of price over
// DISPENSED transactions and nothing else (BR-03), and that price is the copy
// taken when the purchase started (BR-05). So a price change today never
// rewrites what last month earned, and an attempt that failed or was cancelled
// is counted as an attempt, never as money.

// ReportFilter is the span and the machine a figure is measured over. Start and
// End are UTC instants, half-open: created_at >= Start AND created_at < End.
// MachineID 0 means every machine.
type ReportFilter struct {
	Start, End time.Time
	MachineID  int64
}

// SummaryWindows are the four spans the tiles at the top of the admin home
// compare (FR-17). They are worked out in the server's zone by the usecase and
// handed down as instants, so the repository does no calendar arithmetic.
//
// YesterdayStart and YesterdayUntil bound "the same time yesterday": yesterday
// up to the time of day it is now, which is the only comparison with today that
// means anything before the day is over.
type SummaryWindows struct {
	TodayStart     time.Time
	YesterdayStart time.Time
	YesterdayUntil time.Time
	Last7Start     time.Time
	MonthStart     time.Time
}

// Summary is the four tiles.
type Summary struct {
	Today             Totals
	SameTimeYesterday Totals
	Last7             Totals
	Month             Totals
}

// RecapTotals is the recap's headline figures: what was earned, and what needs
// somebody to look at it.
type RecapTotals struct {
	Revenue int64
	Sales   int64
	// Failed is dispenses the machine could not complete (BR-07).
	Failed int64
	// Reported is sales a customer said did not come out (FR-09). They are
	// still revenue; they are flagged, not deducted (BR-03).
	Reported int64
	// Cancelled counts CANCELLED and EXPIRED together: from the figures' point
	// of view, a customer who changed their mind and one who walked away are
	// the same non-sale.
	Cancelled int64
}

// SeriesPoint is one bar of the recap chart.
type SeriesPoint struct {
	// Period is the bucket's label: a day, the Monday of a week, or a month.
	Period string
	Start  time.Time
	Totals Totals
}

// MachineRevenue is one machine's line in the recap, with enough of the machine
// to show the row without a second lookup.
type MachineRevenue struct {
	MachineID  int64
	Code       string
	Name       string
	Location   string
	IsActive   bool
	Totals     Totals
	LastSaleAt *time.Time
	LastSeenAt *time.Time
}

// ProductRevenue is one product's line in the recap breakdown.
type ProductRevenue struct {
	ProductID int64
	Name      string
	Totals    Totals
}

// SlotRevenue is one slot's line in the recap breakdown.
//
// CurrentProduct is what the slot holds now, which is not necessarily what it
// sold over the period: the figures are for the coil, and what was in it may
// have changed at a visit. It is nil for a slot that holds nothing today.
type SlotRevenue struct {
	MachineID      int64
	MachineCode    string
	SlotNumber     int
	CurrentProduct *string
	Totals         Totals
}

// BreakdownRow is one line of the recap's breakdown table, whichever way it is
// broken down. Key identifies the row for the screen; Label is what it says.
type BreakdownRow struct {
	Key     string
	Label   string
	Revenue int64
	Sales   int64
}

// Recap is the whole revenue recap (UC-06, FR-17, FR-18).
type Recap struct {
	From, To  string
	GroupBy   string
	Totals    RecapTotals
	Series    []SeriesPoint
	ByMachine []MachineRevenue
	// TopProducts is the best few by revenue, always by product however the
	// breakdown below is grouped: it answers "what sells", which is a different
	// question from "where did the money come from".
	TopProducts []BreakdownRow
	Breakdown   []BreakdownRow
}
