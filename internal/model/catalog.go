package model

// Catalog is what one machine's kiosk shows (FR-01): every slot with its
// product and whether it can be bought right now.
type Catalog struct {
	CatalogVersion int64
	Machine        Machine
	Slots          []CatalogSlot
}

// CatalogSlot is one slot as the kiosk shows it.
type CatalogSlot struct {
	SlotNumber int
	Available  bool
	Product    *Product
}

// Heartbeat is the answer to an agent's periodic contact (FR-28). The agent
// compares CatalogVersion with its own and reloads early when they differ.
type Heartbeat struct {
	ServerTime     int64
	CatalogVersion int64
}
