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
