// Package catalog owns what a kiosk shows and the agent's heartbeat (UC-01).
//
// The availability rule (BR-04) lives here rather than in a SQL expression, so
// it can be read and tested: a slot sells only when the machine is active, the
// slot holds an active product, the slot is not blocked by a failed dispense,
// and its count minus the items already claimed by open transactions is above
// zero.
package catalog

import (
	"context"
	"log/slog"
	"time"

	"vending/internal/common/clock"
	"vending/internal/model"
	"vending/internal/repository/machine"
	"vending/internal/repository/uow"
)

//go:generate mockgen -source=catalog.go -destination=mock/catalog_mock.go -package=mock

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// Get returns the catalog of one machine (FR-01).
	Get(ctx context.Context, m model.Machine) (model.Catalog, error)
	// Heartbeat records the agent's contact and reports the current catalog
	// version, so the agent reloads early when an admin changed something.
	Heartbeat(ctx context.Context, m model.Machine) (model.Heartbeat, error)
}

// Service is the implementation. It depends only on repository interfaces.
type Service struct {
	// uow is for the catalog read, whose version and slots must come from one
	// snapshot, or a kiosk could be handed slots that no longer match the
	// version it is told to quote.
	uow uow.Repository
	// machines is bound to the pool, for the reads and writes that need no
	// transaction of their own.
	machines      machine.Repository
	clock         clock.Clock
	paymentWindow time.Duration
	log           *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in cmd/vending-server.
func New(u uow.Repository, machines machine.Repository, c clock.Clock, paymentWindow time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uow: u, machines: machines, clock: c, paymentWindow: paymentWindow, log: log}
}

// Get reads the catalog in one snapshot and applies BR-04 to every slot.
func (s *Service) Get(ctx context.Context, m model.Machine) (model.Catalog, error) {
	var out model.Catalog
	err := s.uow.Read(ctx, func(tx uow.Tx) error {
		var err error
		out, err = s.read(ctx, tx, m.ID)
		return err
	})
	return out, err
}

// read builds the catalog from one snapshot. It is also what a stale-catalog
// refusal returns, which is why it takes the unit of work rather than opening
// its own (purchase calls it through CatalogIn).
func (s *Service) read(ctx context.Context, tx uow.Tx, machineID int64) (model.Catalog, error) {
	rec, err := tx.Machines().Get(ctx, machineID)
	if err != nil {
		return model.Catalog{}, err
	}
	slots, err := tx.Slots().ListByMachine(ctx, machineID)
	if err != nil {
		return model.Catalog{}, err
	}
	open, err := tx.Transactions().CountOpenBySlot(ctx, machineID, s.clock.Now().Add(-s.paymentWindow))
	if err != nil {
		return model.Catalog{}, err
	}

	out := model.Catalog{
		CatalogVersion: rec.CatalogVersion,
		Machine:        rec.Identity(),
		Slots:          make([]model.CatalogSlot, 0, len(slots)),
	}
	for _, sl := range slots {
		out.Slots = append(out.Slots, model.CatalogSlot{
			SlotNumber: sl.SlotNumber,
			Available:  sl.Sellable(rec.IsActive, open[sl.ID]),
			Product:    sl.Product,
		})
	}
	return out, nil
}

// CatalogIn reads the catalog inside a unit of work the caller already opened.
// The purchase story uses it to answer CATALOG_STALE with the fresh catalog.
func (s *Service) CatalogIn(ctx context.Context, tx uow.Tx, machineID int64) (model.Catalog, error) {
	return s.read(ctx, tx, machineID)
}

// Heartbeat records the contact time (FR-28) and reports the catalog version.
func (s *Service) Heartbeat(ctx context.Context, m model.Machine) (model.Heartbeat, error) {
	now := s.clock.Now()
	if err := s.machines.TouchSeen(ctx, m.ID, now); err != nil {
		return model.Heartbeat{}, err
	}
	rec, err := s.machines.Get(ctx, m.ID)
	if err != nil {
		return model.Heartbeat{}, err
	}
	return model.Heartbeat{ServerTime: now.UnixMilli(), CatalogVersion: rec.CatalogVersion}, nil
}
