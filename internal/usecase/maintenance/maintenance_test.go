package maintenance_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/model"
	healthmock "vending/internal/repository/health/mock"
	reportmock "vending/internal/repository/report/mock"
	sessionmock "vending/internal/repository/session/mock"
	txmock "vending/internal/repository/transaction/mock"
	"vending/internal/usecase/maintenance"
)

// What these tests are about is the cutoffs. A sweep is one call to one
// repository, and the only thing this package decides is which instant the
// sweep measures against -- which is exactly where an off-by-one window would
// expire a payment somebody is still making.

// 22 Oct 2026, 10:00 WIB.
var t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)

const (
	paymentWindow = 60 * time.Second
	adminIdle     = 30 * time.Minute
	operatorIdle  = 2 * time.Minute
)

type fixture struct {
	txs      *txmock.MockRepository
	sessions *sessionmock.MockRepository
	reports  *reportmock.MockRepository
	store    *healthmock.MockRepository
	uc       *maintenance.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		txs:      txmock.NewMockRepository(ctrl),
		sessions: sessionmock.NewMockRepository(ctrl),
		reports:  reportmock.NewMockRepository(ctrl),
		store:    healthmock.NewMockRepository(ctrl),
	}
	f.uc = maintenance.New(f.txs, f.sessions, f.reports, f.store,
		clock.New(func() time.Time { return t0 }),
		paymentWindow, adminIdle, operatorIdle, nil)
	return f, ctrl
}

// A payment is swept once it could no longer have been paid: one window ago,
// not now. The window goes down too, because each row is stamped as having
// ended when its own window ran out rather than when the sweep ran (BR-06).
func TestExpirePendingSweepsOneWindowBack(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.txs.EXPECT().ExpireStaleAll(gomock.Any(), t0.Add(-paymentWindow), paymentWindow).Return(int64(3), nil)

	n, err := f.uc.ExpirePending(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("ExpirePending = %d, %v; want 3 and no error", n, err)
	}
}

// The two kinds of session are given very different patience (BR-13), and both
// cutoffs come from one clock reading so they cannot be measured a tick apart.
func TestCleanupSessionsUsesBothIdleLimits(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.sessions.EXPECT().DeleteIdle(gomock.Any(), t0.Add(-adminIdle), t0.Add(-operatorIdle)).Return(int64(2), nil)

	n, err := f.uc.CleanupSessions(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("CleanupSessions = %d, %v; want 2 and no error", n, err)
	}
}

// Reconcile carries the count of what does not add up, so the screen and the
// command line cannot report a different number from the same rows (NFR-05).
func TestReconcileCountsMismatches(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.reports.EXPECT().Reconcile(gomock.Any()).Return([]model.SlotReconciliation{
		{MachineCode: "VM-01", SlotNumber: 1, Matches: true},
		{MachineCode: "VM-01", SlotNumber: 2, Expected: 4, Actual: 3},
		{MachineCode: "VM-02", SlotNumber: 1, NeverRestocked: true, Matches: true},
	}, nil)

	out, err := f.uc.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Slots) != 3 || out.Mismatches != 1 {
		t.Fatalf("got %d slots and %d mismatches, want 3 and 1", len(out.Slots), out.Mismatches)
	}
}
