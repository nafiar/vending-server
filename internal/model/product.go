package model

import "time"

// MaxPrice is the ceiling on a product price (FR-19).
const MaxPrice = 100_000_000

// Product is one item the machines sell.
type Product struct {
	ID          int64
	Name        string
	Description string
	Price       int64
	ImageURL    *string
	IsActive    bool // false: archived, so no slot may sell it (BR-02)
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MaxProductNameRunes and MaxProductDescriptionRunes bound what an admin can
// type, so a name stays readable on a kiosk tile and in a change-log row.
const (
	MaxProductNameRunes        = 100
	MaxProductDescriptionRunes = 1000
)

// SlotUse is one place a product is loaded or planned, as the admin's product
// list shows it. It is why a product cannot simply be archived: something is
// still selling it (BR-10).
type SlotUse struct {
	MachineCode string
	SlotNumber  int
	Planned     bool // the product is the slot's planned next product, not what it holds
}

// ProductDetail is a product with everywhere it is used, which is what the
// admin's product screen shows and what every product write answers with.
type ProductDetail struct {
	Product Product
	UsedIn  []SlotUse
}
