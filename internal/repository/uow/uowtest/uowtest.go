// Package uowtest provides a unit of work for usecase tests.
//
// It exists because of a problem that showed up the second time uow.Tx grew: a
// hand-written fake in every usecase's test meant that adding one getter to the
// port broke every one of them, for no reason to do with what they test. One
// bundle here, kept in step with the port, means a test declares only the
// repositories it actually uses and ignores the rest.
//
// Do and Read simply call fn with the bundle, which is the behaviour a usecase
// is entitled to rely on: what it is handed is bound to one unit of work. A
// repository left nil will panic if the code under test reaches for it, which
// is what you want — it says the usecase touched something the test did not
// expect it to.
package uowtest

import (
	"context"

	"vending/internal/repository/audit"
	"vending/internal/repository/machine"
	"vending/internal/repository/product"
	"vending/internal/repository/restock"
	"vending/internal/repository/slot"
	"vending/internal/repository/transaction"
	"vending/internal/repository/uow"
	"vending/internal/repository/user"
)

// Bundle is a uow.Repository and a uow.Tx in one: the test fills in the
// repositories it needs, usually with generated mocks, and passes it straight
// to a usecase constructor. Its getters mirror uow.Tx exactly, so when the port
// grows this file is the one place that has to follow.
type Bundle struct {
	MachineRepo     machine.Repository
	SlotRepo        slot.Repository
	TransactionRepo transaction.Repository
	UserRepo        user.Repository
	RestockRepo     restock.Repository
	ProductRepo     product.Repository
	AuditRepo       audit.Repository

	// Attempts counts how often a unit of work has been opened, of either kind,
	// so a test can assert that a retry did or did not happen, or that a read
	// that needs no transaction did not open one.
	Attempts int

	// RetryFirst makes the first Do run its function twice and keep only what
	// the second attempt did, which is what the mysql driver does when InnoDB
	// aborts a transaction. It is how a test checks that a usecase carries
	// nothing over from an attempt that was rolled back.
	RetryFirst bool
	retried    bool
}

var _ uow.Repository = (*Bundle)(nil)
var _ uow.Tx = (*Bundle)(nil)

func (b *Bundle) Do(ctx context.Context, fn func(uow.Tx) error) error {
	b.Attempts++
	if b.RetryFirst && !b.retried {
		b.retried = true
		if err := fn(b); err != nil {
			return err
		}
	}
	return fn(b)
}

func (b *Bundle) Read(ctx context.Context, fn func(uow.Tx) error) error {
	b.Attempts++
	return fn(b)
}

func (b *Bundle) Machines() machine.Repository         { return b.MachineRepo }
func (b *Bundle) Slots() slot.Repository               { return b.SlotRepo }
func (b *Bundle) Transactions() transaction.Repository { return b.TransactionRepo }
func (b *Bundle) Users() user.Repository               { return b.UserRepo }
func (b *Bundle) Restocks() restock.Repository         { return b.RestockRepo }
func (b *Bundle) Products() product.Repository         { return b.ProductRepo }
func (b *Bundle) Audit() audit.Repository              { return b.AuditRepo }
