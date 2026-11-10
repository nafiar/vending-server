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
}
