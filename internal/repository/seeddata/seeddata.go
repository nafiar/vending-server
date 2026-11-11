// Package seeddata writes the demo dataset. The port is here; the drivers live
// in subpackages.
//
// It exists so that nothing else has to hand out a raw connection pool. The
// demo load is the one operation in the system that writes every table at once
// and empties them all again, and before this port it was done by passing
// *sql.DB to a package that then wrote SQL outside every rule the rest of the
// system follows (docs/REARCHITECTURE.md §7).
//
// Being a port does not make it ordinary: it is for a demo server, it trusts
// the ids it is given, and it is the only place a TRUNCATE lives. Keeping that
// behind one named interface is what makes it obvious how much power it has.
package seeddata

import (
	"context"

	"vending/internal/model"
)

//go:generate mockgen -source=seeddata.go -destination=mock/seeddata_mock.go -package=mock

// Repository is the bulk load.
type Repository interface {
	// MachineKeyHashes is every machine's code and the hash of its key, read
	// before a reset so that a machine coming back under the same code can keep
	// the key a kiosk is already configured with.
	MachineKeyHashes(ctx context.Context) (map[string]string, error)

	// CountOccupied counts the rows that would make a load unsafe: the users,
	// machines and products together. A caller uses it to refuse to seed over
	// somebody's real data; zero means the database is empty enough to fill.
	CountOccupied(ctx context.Context) (int, error)

	// Wipe deletes every row in every table.
	//
	// It is not part of Write and cannot be: emptying the tables uses TRUNCATE,
	// which commits on its own, so there is no transaction that could cover
	// both the wipe and the load. A reset is therefore two steps, and a failure
	// between them leaves an empty database rather than a mixed one -- which is
	// the right way round for a demo server.
	Wipe(ctx context.Context) error

	// Write inserts a whole dataset in one transaction: either the demo data is
	// all there or none of it is.
	Write(ctx context.Context, d model.DemoData) error
}
