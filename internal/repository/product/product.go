// Package product reads and writes the products an admin manages. The port is
// here; the drivers live in subpackages.
//
// A product is never deleted, only archived (BR-10, TC-25): past transactions
// point at it, and a sale that loses what it sold is not a record of anything.
// So there is no Delete here, only SetActive.
package product

import (
	"context"
	"time"

	"vending/internal/model"
)

//go:generate mockgen -source=product.go -destination=mock/product_mock.go -package=mock

// Repository is the products table as the rules see it.
type Repository interface {
	// Get returns one product, or an error wrapping model.ErrNoRecord.
	Get(ctx context.Context, id int64) (model.Product, error)

	// List returns the products, archived ones last and only when asked for,
	// then by name. The order is part of what the admin's list shows.
	List(ctx context.Context, includeArchived bool) ([]model.Product, error)

	// LockForUpdate is Get holding an exclusive lock on the row until the unit
	// of work ends, so a save reads the values it is about to log as the
	// "before" and nothing can change them in between.
	LockForUpdate(ctx context.Context, id int64) (model.Product, error)

	// LockShared reads one product and holds a shared lock on its row until the
	// unit of work ends, so it cannot be archived while a rule is acting on the
	// fact that it is active.
	//
	// Planning a product into a slot takes this: an archive of the same product
	// then waits until the plan is saved, and sees it, rather than the two
	// deciding at once that each is fine (BR-10).
	LockShared(ctx context.Context, id int64) (model.Product, error)

	// Create adds a product and returns its id. It reads Name, Description,
	// Price, ImageURL, CreatedAt and UpdatedAt; a new product is always active.
	Create(ctx context.Context, p model.Product) (int64, error)

	// Update writes the editable fields of a product: Name, Description, Price,
	// ImageURL and UpdatedAt, by ID. Whether it is active is not among them,
	// because archiving is its own decision with its own rule.
	Update(ctx context.Context, p model.Product) error

	// SetActive archives a product or brings it back (BR-10).
	SetActive(ctx context.Context, id int64, active bool, at time.Time) error

	// Usage maps product id to every slot that holds or plans it, ordered by
	// machine code then slot number. Pass 0 for every product, which is what
	// the admin's list needs, or one id to answer whether that one can be
	// archived.
	//
	// A slot that both holds one product and plans another appears under both.
	Usage(ctx context.Context, productID int64) (map[int64][]model.SlotUse, error)
}
