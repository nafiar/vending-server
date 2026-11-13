// Package uow runs a group of repository calls as one atomic unit.
//
// It exists because the rules of this system are only correct when several
// repositories commit together: a purchase writes a transaction row and takes
// an item off a slot, a restock visit rewrites every slot of a machine and
// raises its catalog version, and every change an admin makes writes its
// audit_logs row beside it (FR-26). Splitting those across repositories that
// each hold the pool would lose the guarantee.
//
// The callback is handed a Tx, a bundle of repositories bound to this unit, not
// a driver transaction: the port stays free of driver types, so a usecase test
// passes a Tx made of mocks.
package uow

import "context"

//go:generate mockgen -source=uow.go -destination=mock/uow_mock.go -package=mock

// Repository is the port the usecases depend on.
type Repository interface {
	// Do runs fn in a transaction and commits it when fn returns nil. An
	// implementation may retry fn when the storage engine aborts it, so fn must
	// not keep state from a failed attempt: write to variables the caller
	// declared, and reset them at the top of fn.
	Do(ctx context.Context, fn func(Tx) error) error

	// Read runs fn in a read-only transaction, so every statement in it sees
	// one snapshot.
	Read(ctx context.Context, fn func(Tx) error) error
}
