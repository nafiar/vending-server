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
