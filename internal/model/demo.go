package model

import "time"

// The demo dataset: three machines with about 90 days of trading, written in
// one go so a demo server can be filled or reset from nothing.
//
// These types are flat and dumb on purpose. Everything that makes the data
// plausible -- when the visits happened, which slot sold what, how a price
// change in the past leaves today's figures alone -- is worked out by
// usecase/demo, and what lands here is the finished rows. The repository then
// writes them and reads nothing back into a rule.
//
// Ids are set by the generator rather than by the database, because the data
// refers to itself: a transaction names the slot it came out of and a visit
// names its lines. Writing it without that would mean a round trip per row.

// DemoUser is a demo account. The password is already hashed: hashing is the
// business layer's job (common/security), not the driver's.
type DemoUser struct {
	ID           int64
	Name         string
	Username     string
	PasswordHash string
	Role         string
	IsActive     bool
	CreatedAt    time.Time
}

// DemoProduct is a demo product with its placeholder image already stored.
type DemoProduct struct {
	ID          int64
	Name        string
	Description string
	Price       int64
	ImageURL    string
	IsActive    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DemoMachine is a demo machine. KeyHash is the hash of its machine key, which
// a reset keeps when the machine comes back under the same code, so a kiosk
// already set up with its key keeps working.
type DemoMachine struct {
	ID             int64
	Code           string
	Name           string
	Location       string
	KeyHash        string
	CatalogVersion int64
	LastSeenAt     time.Time
	CreatedAt      time.Time
}

// DemoSlot is one coil of a demo machine, as it stands at the end of the
// simulated history. NextProductID is a planned product change (FR-16), nil
// when none is planned.
type DemoSlot struct {
	ID            int64
	MachineID     int64
	Number        int
	ProductID     int64
	NextProductID *int64
	Capacity      int
	Qty           int
	NeedsCheck    bool
}

// DemoTransaction is one simulated purchase attempt. The three later times are
// nil for an attempt that never reached them, exactly as a real row would be.
type DemoTransaction struct {
	ID                 string
	MachineID          int64
	SlotID             int64
	ProductID          int64
	Price              int64
	Status             string
	FailureReason      *string
	CreatedAt          time.Time
	PaidAt             *time.Time
	FinishedAt         *time.Time
	CustomerReportedAt *time.Time
}

// DemoVisit is one simulated operator visit with its lines per slot. The lines
// travel with the visit because a visit without them is not one.
type DemoVisit struct {
	ID         int64
	ClientID   string
	MachineID  int64
	UserID     int64
	StartedAt  time.Time
	FinishedAt time.Time
	Items      []DemoVisitItem
}

// DemoVisitItem is one slot's line of a simulated visit.
type DemoVisitItem struct {
	SlotID         int64
	ProductID      int64
	QtyBefore      int
	QtyAfter       int
	ProductChanged bool
}

// DemoAudit is one change-log row of the simulated history, so that the change
// log on a demo server has something in it to look at.
type DemoAudit struct {
	ActorID    int64
	Action     string
	EntityType string
	EntityID   int64
	Label      string
	Before     map[string]any // nil for a CREATE
	After      map[string]any
	At         time.Time
}

// DemoData is the whole dataset, written in one transaction. The order of the
// fields is the order it has to be written in: a slot needs its machine, a
// transaction needs its slot.
type DemoData struct {
	Users        []DemoUser
	Products     []DemoProduct
	Machines     []DemoMachine
	Slots        []DemoSlot
	Visits       []DemoVisit
	Transactions []DemoTransaction
	Audits       []DemoAudit
}

// DemoResult is what a load created, for the command line to print and the
// admin web to confirm.
//
// MachineKeys maps a machine code to its new key, and is empty for a machine
// whose key a reset kept: there is no new key to show, and the old one still
// works. A key is only ever shown here, at the moment it is made (FR-23).
type DemoResult struct {
	// Users is one "username / password (role)" line per demo account, which is
	// the only place these passwords are ever printed.
	Users        []string
	MachineKeys  map[string]string
	Transactions int
	Visits       int
}
