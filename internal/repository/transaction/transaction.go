// Package transaction reads and writes the purchases. The port is here; the
// drivers live in subpackages.
package transaction

import (
	"context"
	"time"
)

//go:generate mockgen -source=transaction.go -destination=mock/transaction_mock.go -package=mock

// Repository is the transactions table as the rules see it.
type Repository interface {
	// CountOpenBySlot counts, per slot id, the transactions of one machine that
	// have claimed an item but not finished with it: PAID, or PENDING started
	// at or after pendingSince (the start of the payment window, BR-06).
	//
	// A slot with no open transaction is absent from the result, so a caller
	// reads a missing key as zero.
	CountOpenBySlot(ctx context.Context, machineID int64, pendingSince time.Time) (map[int64]int, error)
}
