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
