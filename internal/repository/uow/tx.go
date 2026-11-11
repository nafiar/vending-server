package uow

import (
	"vending/internal/repository/audit"
	"vending/internal/repository/machine"
	"vending/internal/repository/product"
	"vending/internal/repository/restock"
	"vending/internal/repository/session"
	"vending/internal/repository/slot"
	"vending/internal/repository/transaction"
	"vending/internal/repository/user"
)

// Tx is the transaction-scoped bundle: every getter returns a repository bound
// to this unit of work, so everything written through them commits together.
//
// Callers that lock more than one thing follow the system-wide lock order:
// machine, then slot, then transaction (see repository/machine).
//
// This interface grows as each slice lands. If it ever gets so wide that a
// usecase depends on far more than it uses, the next step is for each usecase
// to declare the narrow subset it needs; the driver's bundle satisfies those
// structurally, with no change here (docs/REARCHITECTURE.md §8).
type Tx interface {
	Machines() machine.Repository
	Slots() slot.Repository
	Transactions() transaction.Repository
	Users() user.Repository
	Restocks() restock.Repository
	Products() product.Repository
	Sessions() session.Repository
	// Audit is in this bundle and nowhere else: a change-log row has to commit
	// with the change it records (FR-26).
	Audit() audit.Repository
}
