// Package maintenance holds the work nobody asks for: the sweeps that keep the
// data honest while the system runs, and the two checks that report on it.
//
// These used to be methods on the one service, and they are a slice of their
// own for a reason: every other usecase answers a request from a person or a
// kiosk, while these answer a clock. That makes them the only usecases whose
// caller is a ticker (delivery/scheduler), and keeping them together is what
// lets that ticker depend on one small port instead of on the whole system.
//
// Nothing here decides anything a purchase depends on. An expired payment is
// already unpayable by the time it is swept (BR-06), and a session past its
// idle timeout is already refused (BR-13); the sweeps only stop the rows piling
// up and stop an abandoned payment holding its item. So none of this needs a
// lock, and a sweep that fails is logged and tried again on the next tick.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"vending/internal/common/clock"
	"vending/internal/model"
	"vending/internal/repository/health"
	"vending/internal/repository/report"
	"vending/internal/repository/session"
	"vending/internal/repository/transaction"
)

//go:generate mockgen -source=maintenance.go -destination=mock/maintenance_mock.go -package=mock

// UseCase is the port the scheduler and the delivery layer depend on.
type UseCase interface {
	// ExpirePending ends every PENDING transaction past its payment window and
	// returns how many (BR-06).
	ExpirePending(ctx context.Context) (int64, error)

	// CleanupSessions ends every session past its idle timeout and returns how
	// many (BR-13).
	CleanupSessions(ctx context.Context) (int64, error)

	// Reconcile checks every slot's count against its own history (NFR-05).
	Reconcile(ctx context.Context) (model.Reconciliation, error)

	// Ping reports whether the store is reachable, for /api/health.
	Ping(ctx context.Context) error
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	transactions transaction.Repository
	sessions     session.Repository
	reports      report.Repository
	store        health.Repository
	clock        clock.Clock

	// The three timeouts the sweeps measure, straight from the configuration:
	// how long a payment screen may hold an item, and how long each kind of
	// session may sit idle.
	paymentWindow time.Duration
	adminIdle     time.Duration
	operatorIdle  time.Duration

	log *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(
	transactions transaction.Repository,
	sessions session.Repository,
	reports report.Repository,
	store health.Repository,
	c clock.Clock,
	paymentWindow, adminIdle, operatorIdle time.Duration,
	log *slog.Logger,
) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		transactions: transactions, sessions: sessions, reports: reports, store: store,
		clock: c, paymentWindow: paymentWindow, adminIdle: adminIdle, operatorIdle: operatorIdle,
		log: log,
	}
}

// ExpirePending sweeps abandoned payments (BR-06).
//
// The cutoff is one payment window ago, so a transaction is only swept once it
// could no longer have been paid. The window itself is passed down because each
// row is stamped as having ended when its own window ran out, not when the
// sweep got round to it.
func (s *Service) ExpirePending(ctx context.Context) (int64, error) {
	return s.transactions.ExpireStaleAll(ctx, s.clock.Now().Add(-s.paymentWindow), s.paymentWindow)
}

// CleanupSessions sweeps idle sessions (BR-13). Both cutoffs come from one
// clock reading, so the two kinds are measured against the same instant.
func (s *Service) CleanupSessions(ctx context.Context) (int64, error) {
	now := s.clock.Now()
	return s.sessions.DeleteIdle(ctx, now.Add(-s.adminIdle), now.Add(-s.operatorIdle))
}

// Reconcile checks the bookkeeping and counts what does not add up (NFR-05).
func (s *Service) Reconcile(ctx context.Context) (model.Reconciliation, error) {
	slots, err := s.reports.Reconcile(ctx)
	if err != nil {
		return model.Reconciliation{}, err
	}
	return model.Tallied(slots), nil
}

func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }
