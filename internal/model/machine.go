package model

import "time"

// Machine is the Kiosk Agent of one machine, taken from its machine key. It is
// the identity a machine request carries, so it holds no mutable state: a rule
// that needs the catalog version or whether the machine is active reads the
// MachineRecord from the repository.
type Machine struct {
	ID   int64
	Code string
	Name string
}

// MachineRecord is the machines row: what an admin manages and what the rules
// read. CatalogVersion rises on every change that a kiosk must notice, which is
// how a stale screen is refused a sale (FR-04, TC-10).
//
// The last sale is not here: it is a figure the recap works out from the
// transactions, not something the machines row holds.
type MachineRecord struct {
	ID             int64
	Code           string
	Name           string
	Location       string // may be empty: the column is nullable
	IsActive       bool
	CatalogVersion int64
	CreatedAt      time.Time
	LastSeenAt     *time.Time
}

// Identity is the record as a request's machine identity.
func (m MachineRecord) Identity() Machine {
	return Machine{ID: m.ID, Code: m.Code, Name: m.Name}
}

// Machine code and name limits (FR-21).
const (
	MaxMachineNameRunes     = 100
	MaxMachineLocationRunes = 150
	MaxSlotCapacity         = 30
)

// Totals is a revenue figure: the sum of price over DISPENSED transactions.
// Only a dispensed sale counts as revenue (BR-03).
type Totals struct {
	Revenue int64
	Sales   int64
}

// SlotStats is what the admin's machine card says about a machine's slots: how
// many there are, how many have nothing to sell, and how many a failed dispense
// has blocked.
type SlotStats struct {
	Count   int
	Empty   int // count 0, or no product loaded
	Blocked int // needs_check: a dispense failed and nobody has looked yet
}

// MachineDetail is a machine as the admin manages it: the record, the figures
// its card shows, and its slots when one machine is being looked at.
type MachineDetail struct {
	Machine MachineRecord
	Slots   SlotStats
	// Month is this calendar month's revenue so far, in the server's zone.
	Month Totals
	// LastSaleAt is when the machine last dispensed something, nil if never.
	LastSaleAt *time.Time
	// Coils is the machine's slots, filled in only when one machine was asked
	// for: the list of machines would be a hundred times larger with them.
	Coils []Slot
}

// MachineWithKey is a machine together with its machine key. The key is shown
// exactly once, when it is created or rotated: only its hash is stored, so the
// server cannot show it again (FR-21, FR-23).
type MachineWithKey struct {
	Machine MachineDetail
	APIKey  string
}
