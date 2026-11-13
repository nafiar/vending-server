package model

import "time"

// Transaction states (PRD, "Purchase sequence and transaction states"). A final
// state never changes (BR-11).
const (
	StatusPending   = "PENDING"
	StatusPaid      = "PAID"
	StatusDispensed = "DISPENSED"
	StatusFailed    = "FAILED"
	StatusCancelled = "CANCELLED"
	StatusExpired   = "EXPIRED"
)

// IsFinal reports whether a transaction in this state can still change.
func IsFinal(status string) bool {
	switch status {
	case StatusDispensed, StatusFailed, StatusCancelled, StatusExpired:
		return true
	}
	return false
}

// Transaction is one purchase: what was bought, from where, for how much, and
// how far it got. The price is a copy taken when the purchase started, so a
// later price change never rewrites a past sale (BR-05).
//
// The three times are pointers because each is set only once the purchase
// reaches that point: a PENDING transaction has none of them.
type Transaction struct {
	ID         string
	MachineID  int64
	SlotID     int64
	SlotNumber int
	Status     string
	Price      int64
	// FailureReason is what the agent reported for a FAILED dispense
	// (MOTOR_TIMEOUT, NO_ACK, …), nil for every other status.
	FailureReason *string
	CreatedAt     time.Time
	PaidAt        *time.Time
	FinishedAt    *time.Time
	// CustomerReportedAt is when the customer pressed "Produk tidak keluar?"
	// on a sale that the machine recorded as dispensed (FR-09, BR-03).
	CustomerReportedAt *time.Time
	// Product is the product as it was when the purchase started.
	Product Product
}

// NewPurchase is a purchase just started (FR-04): the transaction, and how long
// the kiosk may still take to pay for it before it expires (BR-06).
//
// ExpiresIn is zero once the transaction is no longer PENDING, so a repeated
// request for one already paid or cancelled does not restart the clock.
type NewPurchase struct {
	Transaction Transaction
	ExpiresIn   time.Duration
}

// TransactionStatuses are the states the history may be filtered by: every
// state a purchase can be in, and nothing else.
var TransactionStatuses = map[string]bool{
	StatusPending: true, StatusPaid: true, StatusDispensed: true,
	StatusFailed: true, StatusCancelled: true, StatusExpired: true,
}

// The review filters of the transaction history (FR-24). They answer the
// question an admin actually opens the screen with, which is not a status:
//
//   - FlagProblem is "what went wrong": a dispense that failed (BR-07), or a
//     sale a customer said never came out (FR-09).
//   - FlagReported is the narrower "what did a customer complain about".
const (
	FlagProblem  = "problem"
	FlagReported = "reported"
)

// TransactionFilter is one page of the transaction history. Start and End are
// UTC instants, half-open: CreatedAt >= Start AND CreatedAt < End. A zero
// MachineID, SlotNumber, Status or Flag means "don't filter on it".
type TransactionFilter struct {
	Start, End time.Time
	MachineID  int64
	SlotNumber int
	Status     string
	Flag       string
	// Limit and Offset are the window to return. The totals below are measured
	// over the whole filtered set regardless of them.
	Limit, Offset int
}

// TransactionHistoryRow is one purchase attempt as the history screen shows it,
// with the machine and the product it sold flattened in so the list needs no
// second lookup. The product name is the product's name now; the price is the
// copy taken when the purchase started (BR-05).
type TransactionHistoryRow struct {
	ID            string
	Status        string
	Price         int64
	MachineID     int64
	MachineCode   string
	MachineName   string
	SlotNumber    int
	ProductID     int64
	ProductName   string
	FailureReason *string
	CreatedAt     time.Time
	// The three times a purchase may not have reached yet.
	PaidAt             *time.Time
	FinishedAt         *time.Time
	CustomerReportedAt *time.Time
}

// TransactionHistory is one page of the history plus the figures for the whole
// filtered set, so the screen can say "showing 25 of 4,812, worth Rp…" without
// a second request.
//
// Total counts every matching attempt; DispensedRevenue and DispensedCount
// count only what was actually sold (BR-03). Page and PageSize are echoed back
// for the screen and are filled in by the usecase, not the repository: which
// page this is was the caller's decision, not the query's finding.
type TransactionHistory struct {
	Total            int64
	DispensedRevenue int64
	DispensedCount   int64
	Page             int
	PageSize         int
	Rows             []TransactionHistoryRow
}
