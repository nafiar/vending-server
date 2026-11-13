// Package scheduler drives the maintenance sweeps on a timer.
//
// It is a delivery layer, exactly like delivery/http: an inbound adapter that
// turns something from outside -- here the passing of time rather than a
// request -- into one call on one usecase port. It decides nothing. What the
// sweeps do, and why, is in usecase/maintenance; what is left here is how often
// to ask and what to write in the log.
//
// Keeping it a package of its own is what makes the ticker visible. A goroutine
// started from main with a select in it is a background job nobody can find,
// and it tends to acquire a second responsibility the first time one is needed.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"vending/internal/usecase/maintenance"
)

// The sweep intervals. Expiring payments is frequent because an abandoned
// payment screen holds its item until it is swept (BR-06), and a customer
// standing at the machine should not have to wait minutes for the last one to
// be released. Sessions are cheap to leave lying about, so they are swept
// rarely; a session past its timeout is already refused either way (BR-13).
const (
	ExpireEvery  = time.Minute
	CleanupEvery = 10 * time.Minute
)

// Scheduler runs the sweeps until its context ends.
type Scheduler struct {
	uc  maintenance.UseCase
	log *slog.Logger
}

// New returns a scheduler over the maintenance usecase.
func New(uc maintenance.UseCase, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{uc: uc, log: log}
}

// Catchup runs the expiry sweep once, now.
//
// It is for start-up: a server that was stopped overnight comes back with
// payment screens still marked PENDING, and the first tick is a minute away.
// Those items are held until something sweeps them, so the sweep happens before
// the first request rather than after it.
func (s *Scheduler) Catchup(ctx context.Context) {
	if n, err := s.uc.ExpirePending(ctx); err != nil {
		s.log.Error("expire pending transactions at start-up", "err", err)
	} else if n > 0 {
		s.log.Info("expired abandoned payments left from the last run", "count", n)
	}
}

// Run sweeps on both timers until ctx is done. It is meant to be run in its own
// goroutine and returns when the context ends, so a shutdown stops it.
//
// A failed sweep is logged and left for the next tick. There is nothing else to
// do with it: no request is waiting, and the work is idempotent -- the same rows
// are still there to sweep in a minute.
func (s *Scheduler) Run(ctx context.Context) {
	expire := time.NewTicker(ExpireEvery)
	cleanup := time.NewTicker(CleanupEvery)
	defer expire.Stop()
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			if n, err := s.uc.ExpirePending(ctx); err != nil {
				s.log.Error("expire pending transactions", "err", err)
			} else if n > 0 {
				s.log.Info("expired abandoned payments", "count", n)
			}
		case <-cleanup.C:
			if n, err := s.uc.CleanupSessions(ctx); err != nil {
				s.log.Error("clean up sessions", "err", err)
			} else if n > 0 {
				s.log.Debug("cleaned up idle sessions", "count", n)
			}
		}
	}
}
