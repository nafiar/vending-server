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
}
