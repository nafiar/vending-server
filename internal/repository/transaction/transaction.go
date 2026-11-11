// Package transaction reads and writes the purchases. The port is here; the
// drivers live in subpackages.
//
// A transaction row is the record of one purchase and the only place the server
// knows an item has been claimed. Two rules follow from that, and they shape
// every method below:
//
//   - A row is never deleted and a final status is never rewritten (BR-11), so
//     there is no Update and no Delete, only the one-way steps of the state
//     machine: Create, MarkPaid, Finish, Fail, MarkReported.
//   - Counting what a slot has already promised is part of deciding a sale, so
//     the count must be taken under a lock. CountOpenShared does; the plain
//     CountOpenBySlot does not, and says so.
package transaction

import (
	"context"
	"time"

	"vending/internal/model"
)

//go:generate mockgen -source=transaction.go -destination=mock/transaction_mock.go -package=mock

// History is the transaction history the admin screen reads (UC-09, FR-24).
//
// It is a port of its own, apart from Repository, because it has nothing to do
// with selling: it takes no lock, decides nothing, and answers one page of
// questions about purchases that are already over. Keeping it separate means
// the port that does sell stays the short list of state-machine steps above,
// where every method has a lock story.
type History interface {
	// List returns one page of attempts, newest first, and the figures for the
	// whole filtered set. It does not fill in Page or PageSize.
	List(ctx context.Context, f model.TransactionFilter) (model.TransactionHistory, error)
}

// Repository is the transactions table as the rules see it.
type Repository interface {
	// CountOpenBySlot counts, per slot id, the transactions of one machine that
	// have claimed an item but not finished with it: PAID, or PENDING started
	// at or after pendingSince (the start of the payment window, BR-06).
	//
	// It takes no lock, so it is a figure to show, not one to sell against: use
	// CountOpenShared for that.
	//
	// A slot with no open transaction is absent from the result, so a caller
	// reads a missing key as zero.
	CountOpenBySlot(ctx context.Context, machineID int64, pendingSince time.Time) (map[int64]int, error)

	// CountOpenShared counts one slot's open transactions under a shared lock.
	//
	// This is the read that makes oversell impossible (TC-11). It sees every
	// purchase committed before this unit of work was granted the slot lock,
	// rather than the snapshot the unit started with, so two requests racing for
	// the last item cannot both count it as free. The caller must already hold
	// the slot's exclusive lock.
	CountOpenShared(ctx context.Context, slotID int64) (int, error)

	// Get returns one transaction with the product it sold, or an error wrapping
	// model.ErrNoRecord when there is none. It takes no lock.
	Get(ctx context.Context, id string) (model.Transaction, error)

	// LockForUpdate is Get holding an exclusive lock on the transaction row
	// until the unit of work ends, so two reports of the same purchase cannot
	// both read it as unfinished and both act.
	LockForUpdate(ctx context.Context, id string) (model.Transaction, error)

	// ExpireStale ends every PENDING transaction on one slot that started before
	// startedBefore, so an abandoned payment stops holding its item (BR-06).
	//
	// Each row is stamped as finished at the end of its own payment window, not
	// at the time of this call: when it ended is a fact about that purchase, and
	// nothing says the sweep ran promptly. window is that payment window.
	ExpireStale(ctx context.Context, slotID int64, startedBefore time.Time, window time.Duration) error

	// Create inserts a new PENDING transaction, and returns an error wrapping
	// model.ErrDuplicateID when that id was already used.
	//
	// It reads t.ID, t.MachineID, t.SlotID, t.Product.ID, t.Price and
	// t.CreatedAt; the status is PENDING, since that is the only state a
	// purchase can start in.
	Create(ctx context.Context, t model.Transaction) error

	// MarkPaid moves a transaction from PENDING to PAID (UC-03).
	MarkPaid(ctx context.Context, id string, at time.Time) error

	// Finish writes a final status that carries no reason: DISPENSED, CANCELLED
	// or EXPIRED. at is when the purchase ended.
	Finish(ctx context.Context, id, status string, at time.Time) error

	// Fail ends a transaction as FAILED with the reason the agent reported
	// (BR-07). It is separate from Finish because a FAILED row without a reason
	// would tell an operator nothing.
	Fail(ctx context.Context, id, reason string, at time.Time) error

	// MarkReported records the customer's "Produk tidak keluar?" on a sale
	// (FR-09). The status does not change: the sale happened and still counts as
	// revenue, it is only flagged for review (BR-03).
	MarkReported(ctx context.Context, id string, at time.Time) error
}
