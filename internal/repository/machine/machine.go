// Package machine reads and writes the machines an admin registers. The port is
// here; the drivers live in subpackages.
//
// Lock order, system-wide: machine, then slot, then transaction. Every caller
// that locks more than one of them takes them in that order, which is what
// keeps two concurrent purchases from deadlocking (BR-06).
package machine

import (
	"context"
	"time"

	"vending/internal/model"
)

//go:generate mockgen -source=machine.go -destination=mock/machine_mock.go -package=mock

// Repository is the machines table as the rules see it. Methods grow with the
// slices that need them; a port carries only what a usecase actually calls.
type Repository interface {
	// Get returns one machine's row, or an error wrapping model.ErrNoRecord
	// when there is none.
	Get(ctx context.Context, id int64) (model.MachineRecord, error)

	// TouchSeen records that this machine's agent made contact (FR-28). It is
	// called often, so the caller decides how rarely to bother.
	TouchSeen(ctx context.Context, id int64, at time.Time) error

	// ByAPIKeyHash returns the machine whose machine key hashes to this value,
	// or an error wrapping model.ErrNoRecord. The key itself is never stored,
	// only its hash, so a leaked table row cannot be used as a key (FR-23,
	// BR-12).
	ByAPIKeyHash(ctx context.Context, keyHash string) (model.MachineRecord, error)

	// LockShared reads one machine and holds a shared lock on its row until the
	// unit of work ends, so the catalog version and the active flag a rule just
	// read cannot change under it while the rule acts on them.
	//
	// It is the first lock a purchase takes, before any slot (BR-06). Other
	// readers may take the same shared lock; a writer waits.
	LockShared(ctx context.Context, id int64) (model.MachineRecord, error)

	// LockForUpdate reads one machine and holds an exclusive lock on its row
	// until the unit of work ends, so nothing else may read or change it.
	//
	// An operator visit takes this rather than LockShared: it is about to raise
	// the catalog version, and the exclusive lock is also what makes two sends
	// of the same visit queue instead of both deciding they are the first.
	LockForUpdate(ctx context.Context, id int64) (model.MachineRecord, error)

	// BumpCatalogVersion raises this machine's catalog version by one, which is
	// how every kiosk is told that what it is showing is out of date (FR-04).
	//
	// It takes an exclusive lock on the machine row, so a caller that also locks
	// a slot calls this first, keeping to the lock order above.
	BumpCatalogVersion(ctx context.Context, id int64) error
}
