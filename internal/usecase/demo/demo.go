// Package demo fills a demo server with data worth looking at: three machines
// with about 90 days of trading, their visits, their failures and their change
// log (UC-13 is not a requirement, this is for showing the system).
//
// The data is simulated event by event rather than made up row by row, and that
// is the whole point of it. Every count in it is the result of a sale or a
// visit that is also in the data, so the bookkeeping check reconciles (NFR-05),
// the recap adds up to the transactions behind it, and a price change twenty
// days ago leaves the sales before it at the old price (BR-05). Data invented
// per table looks right on each screen and contradicts itself across two.
//
// What this package may do is deliberately limited. It builds rows and hands
// them to one port (repository/seeddata); it holds no connection, writes no
// SQL, and cannot empty a table except by asking. Before the rearchitecture
// this was a package handed the raw *sql.DB, which is how it came to be the one
// place in the system writing outside every rule the rest of it follows.
package demo

import (
	"context"
	"errors"
	"time"

	"vending/internal/common/clock"
	"vending/internal/model"
	"vending/internal/repository/imagestore"
	"vending/internal/repository/seeddata"
)

//go:generate mockgen -source=demo.go -destination=mock/demo_mock.go -package=mock

// UseCase is the port the delivery layer and the command line depend on.
type UseCase interface {
	// Load fills the database with the demo dataset.
	//
	// With reset it first deletes every row, keeping the keys of machines that
	// come back under the same code so a configured kiosk keeps working.
	// Without it, a database that already holds users, machines or products is
	// refused: seeding over somebody's real data is not recoverable.
	Load(ctx context.Context, reset bool) (model.DemoResult, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	store  seeddata.Repository
	images imagestore.Repository
	clock  clock.Clock
	// loc is the zone the simulated days are laid out in: a sale "at 09:00"
	// means nine in the morning where the machines are.
	loc *time.Location
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(store seeddata.Repository, images imagestore.Repository, c clock.Clock, loc *time.Location) *Service {
	return &Service{store: store, images: images, clock: c, loc: loc}
}

// ErrNotEmpty is the refusal to seed over existing data.
var ErrNotEmpty = errors.New("the database already has data; run seed-demo -reset to delete it first, or use an empty database")

// The seed of the generator. It is fixed so that two demo servers built from
// this code show the same history: a demo that looks different every time is
// one nobody can be walked through.
const (
	rngSeed1 = 20261013
	rngSeed2 = 22
)

func (s *Service) Load(ctx context.Context, reset bool) (model.DemoResult, error) {
	keptKeys := map[string]string{}
	if reset {
		var err error
		if keptKeys, err = s.store.MachineKeyHashes(ctx); err != nil {
			return model.DemoResult{}, err
		}
		if err := s.store.Wipe(ctx); err != nil {
			return model.DemoResult{}, err
		}
	} else {
		n, err := s.store.CountOccupied(ctx)
		if err != nil {
			return model.DemoResult{}, err
		}
		if n > 0 {
			return model.DemoResult{}, ErrNotEmpty
		}
	}

	data, res, err := s.build(keptKeys)
	if err != nil {
		return model.DemoResult{}, err
	}
	if err := s.store.Write(ctx, data); err != nil {
		return model.DemoResult{}, err
	}
	return res, nil
}
