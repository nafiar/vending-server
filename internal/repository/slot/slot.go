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
}
