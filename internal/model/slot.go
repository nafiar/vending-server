package model

// MaxSlots is the highest slot number a machine can have.
const MaxSlots = 60

// Slot is one coil of one machine: what it holds, how many are in it, and the
// product an operator is to load into it next.
type Slot struct {
	ID         int64
	MachineID  int64
	SlotNumber int
	Capacity   int
	Qty        int
	// NeedsCheck is set when a dispense failed, and blocks the slot until the
	// next operator visit clears it (BR-07).
	NeedsCheck bool
	// Product is what the slot holds now, nil when the slot is empty.
	Product *Product
	// NextProduct is the planned change, applied at the next visit (FR-16).
	NextProduct *Product
}

// Sellable reports whether this slot can sell one item right now, given how
// many open transactions are already holding an item from it (BR-04).
//
// open counts the transactions that have claimed an item but not finished:
// PAID, or PENDING inside the payment window. machineActive is the machine's
// own state, since a deactivated machine sells nothing.
func (s Slot) Sellable(machineActive bool, open int) bool {
	return machineActive &&
		s.Product != nil &&
		s.Product.IsActive &&
		!s.NeedsCheck &&
		s.Qty-open > 0
}
