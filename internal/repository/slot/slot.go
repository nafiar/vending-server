// Package slot reads and writes the slots of a machine. The port is here; the
// drivers live in subpackages.
package slot

import (
	"context"

	"vending/internal/model"
)

//go:generate mockgen -source=slot.go -destination=mock/slot_mock.go -package=mock

// Repository is the slots table as the rules see it.
type Repository interface {
	// ListByMachine returns every slot of one machine in slot order, each with
	// the product it holds and the product planned for it.
	ListByMachine(ctx context.Context, machineID int64) ([]model.Slot, error)

	// LockForUpdate reads one slot and holds an exclusive lock on its row until
	// the unit of work ends, so no other purchase can read the same count and
	// sell the same item (BR-06, TC-11). Returns an error wrapping
	// model.ErrNoRecord when the machine has no such slot.
	//
	// The machine is locked first; this is the second lock in the system-wide
	// order (see repository/machine).
	LockForUpdate(ctx context.Context, machineID int64, slotNumber int) (model.Slot, error)

	// TakeOne removes one item from a slot's count, and reports whether there
	// was one to remove. A false means the count was already zero: the item
	// left the machine but the server's figure disagrees, which is the caller's
	// to report, not to repair (BR-07).
	TakeOne(ctx context.Context, slotID int64) (bool, error)

	// MarkNeedsCheck blocks a slot until an operator's next visit, after a
	// dispense the machine could not complete (BR-07, TC-06).
	MarkNeedsCheck(ctx context.Context, slotID int64) error

	// LockAllByMachine is ListByMachine holding an exclusive lock on every slot
	// row of the machine until the unit of work ends, in slot order.
	//
	// An operator visit rewrites all of them together, so it takes all of them
	// at once; the fixed order means two visits to one machine queue rather
	// than deadlock. The machine is locked first (see repository/machine).
	LockAllByMachine(ctx context.Context, machineID int64) ([]model.Slot, error)

	// MachineIDsHolding returns the ids of the machines that have this product
	// loaded in a slot right now, so their kiosks can be told to reload after
	// its price or name changed (BR-09, TC-10).
	//
	// Slots that only plan the product are left out: nothing is selling at that
	// price yet.
	MachineIDsHolding(ctx context.Context, productID int64) ([]int64, error)

	// Restock sets what a slot holds and how many are in it, clears any planned
	// product change, and unblocks the slot: everything an operator's visit
	// decides about one slot, in one statement, because they are one fact
	// (FR-15, FR-16).
	//
	// productID is nil for a slot left empty.
	Restock(ctx context.Context, slotID int64, productID *int64, qty int) error
}
