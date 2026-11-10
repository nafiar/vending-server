package purchase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/model"
	machinemock "vending/internal/repository/machine/mock"
	slotmock "vending/internal/repository/slot/mock"
	transactionmock "vending/internal/repository/transaction/mock"
	"vending/internal/repository/uow/uowtest"
	catalogmock "vending/internal/usecase/catalog/mock"
	"vending/internal/usecase/purchase"
)

// These tests exist for one reason above all: the purchase rules are only
// correct under a particular lock order, and a lock order is invisible in a
// passing integration test until the day two customers tap at once. With every
// boundary a mock, the order is something a test can state outright
// (docs/REARCHITECTURE.md §2.3).

var (
	t0     = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	window = 60 * time.Second
	vm     = model.Machine{ID: 9, Code: "VM-01", Name: "Depan"}
	txID   = "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
)

// fixture is the usecase with every boundary mocked. The unit of work comes
// from the shared test bundle, so adding a getter to uow.Tx does not break
// tests that never reach for it.
type fixture struct {
	unit         *uowtest.Bundle
	machines     *machinemock.MockRepository
	slots        *slotmock.MockRepository
	transactions *transactionmock.MockRepository
	catalog      *catalogmock.MockUseCase
	uc           *purchase.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		machines:     machinemock.NewMockRepository(ctrl),
		slots:        slotmock.NewMockRepository(ctrl),
		transactions: transactionmock.NewMockRepository(ctrl),
		catalog:      catalogmock.NewMockUseCase(ctrl),
	}
	f.unit = &uowtest.Bundle{MachineRepo: f.machines, SlotRepo: f.slots, TransactionRepo: f.transactions}
	// The pool-bound repository and the one inside the unit are the same type,
	// so one mock serves both.
	f.uc = purchase.New(f.unit, f.transactions, f.catalog, clock.New(func() time.Time { return t0 }), window, nil)
	return f, ctrl
}

func active(version int64) model.MachineRecord {
	return model.MachineRecord{ID: vm.ID, Code: vm.Code, Name: vm.Name, IsActive: true, CatalogVersion: version}
}

func stocked(qty int) model.Slot {
	return model.Slot{
		ID: 55, MachineID: vm.ID, SlotNumber: 1, Capacity: 10, Qty: qty,
		Product: &model.Product{ID: 7, Name: "Gacha", Price: 25000, IsActive: true},
	}
}

func input() purchase.CreateInput {
	return purchase.CreateInput{ID: txID, SlotNumber: 1, CatalogVersion: 12}
}

// code returns the business error code behind err, for a test that cares which
// failure happened rather than how it is worded.
func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Code
}

// ---------------------------------------------------------------- create

// The order these locks are taken in is the whole of BR-06: machine first, then
// the slot, and only then the count that decides the sale. Taken the other way
// round, two purchases on one machine deadlock; counted before the slot lock,
// they both sell the last item.
func TestCreateLocksTheMachineThenTheSlotThenCounts(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
	lockedMachine := f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(12), nil)
	lockedSlot := f.slots.EXPECT().LockForUpdate(gomock.Any(), vm.ID, 1).
		After(lockedMachine).Return(stocked(5), nil)
	expired := f.transactions.EXPECT().
		ExpireStale(gomock.Any(), int64(55), t0.Add(-window), window).
		After(lockedSlot).Return(nil)
	counted := f.transactions.EXPECT().CountOpenShared(gomock.Any(), int64(55)).
		After(expired).Return(0, nil)
	f.transactions.EXPECT().Create(gomock.Any(), gomock.Any()).After(counted).
		DoAndReturn(func(_ context.Context, got model.Transaction) error {
			// The price is copied onto the transaction, not referenced (BR-05).
			if got.ID != txID || got.Price != 25000 || got.Product.ID != 7 {
				t.Errorf("created %+v", got)
			}
			if got.MachineID != vm.ID || got.SlotID != 55 || got.SlotNumber != 1 {
				t.Errorf("created %+v", got)
			}
			if !got.CreatedAt.Equal(t0) {
				t.Errorf("createdAt = %v, want %v", got.CreatedAt, t0)
			}
			return nil
		})

	got, err := f.uc.Create(context.Background(), vm, input())
	if err != nil {
		t.Fatal(err)
	}
	if got.Transaction.Status != model.StatusPending || got.ExpiresIn != window {
		t.Errorf("purchase = %+v", got)
	}
}

// The availability rule is checked again under the slot lock, against the count
// of what is already claimed. Without that, the kiosk's own screen would be the
// only thing standing between two customers and one item (TC-11).
func TestCreateRefusesWhatIsAlreadyClaimed(t *testing.T) {
	cases := []struct {
		name string
		slot model.Slot
		open int
	}{
		{"the last item is held by an open transaction", stocked(1), 1},
		{"the slot is empty", stocked(0), 0},
		{"the slot is blocked by a failed dispense", func() model.Slot {
			s := stocked(5)
			s.NeedsCheck = true
			return s
		}(), 0},
		{"the product has been archived", func() model.Slot {
			s := stocked(5)
			s.Product.IsActive = false
			return s
		}(), 0},
		{"the slot holds nothing", func() model.Slot {
			s := stocked(5)
			s.Product = nil
			return s
		}(), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
			f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(12), nil)
			f.slots.EXPECT().LockForUpdate(gomock.Any(), vm.ID, 1).Return(tc.slot, nil)
			f.transactions.EXPECT().ExpireStale(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			f.transactions.EXPECT().CountOpenShared(gomock.Any(), gomock.Any()).Return(tc.open, nil)
			// No Create is expected: gomock fails the test if one happens.

			_, err := f.uc.Create(context.Background(), vm, input())
			if got := code(t, err); got != model.CodeSoldOut {
				t.Errorf("code = %s, want %s", got, model.CodeSoldOut)
			}
		})
	}
}

// A kiosk quoting a version that has moved on is refused together with the
// fresh catalog, so it can redraw from the same answer (TC-10). Nothing is
// locked beyond the machine and nothing is written.
func TestCreateRefusesAStaleCatalogWithTheFreshOne(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	fresh := model.Catalog{CatalogVersion: 13, Machine: vm}

	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(13), nil)
	f.catalog.EXPECT().Get(gomock.Any(), vm).Return(fresh, nil)

	_, err := f.uc.Create(context.Background(), vm, input())
	if got := code(t, err); got != model.CodeCatalogStale {
		t.Fatalf("code = %s, want %s", got, model.CodeCatalogStale)
	}
	e, _ := model.AsError(err)
	if got, ok := e.Extra["catalog"].(model.Catalog); !ok || got.CatalogVersion != 13 {
		t.Errorf("extra catalog = %#v, want the fresh one", e.Extra["catalog"])
	}
}

func TestCreateRefusesADeactivatedMachine(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	rec := active(12)
	rec.IsActive = false
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(rec, nil)

	_, err := f.uc.Create(context.Background(), vm, input())
	if got := code(t, err); got != model.CodeMachineInactive {
		t.Errorf("code = %s, want %s", got, model.CodeMachineInactive)
	}
}

func TestCreateRefusesASlotTheMachineDoesNotHave(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(12), nil)
	f.slots.EXPECT().LockForUpdate(gomock.Any(), vm.ID, 1).Return(model.Slot{}, model.ErrNoRecord)

	_, err := f.uc.Create(context.Background(), vm, input())
	if got := code(t, err); got != model.CodeNotFound {
		t.Errorf("code = %s, want %s", got, model.CodeNotFound)
	}
}

func TestCreateRefusesAnIDThatIsNotAUUID(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// Nothing is read: the id is refused before any repository is touched.
	in := input()
	in.ID = "bukan-uuid"
	_, err := f.uc.Create(context.Background(), vm, in)
	if got := code(t, err); got != model.CodeInvalidInput {
		t.Errorf("code = %s, want %s", got, model.CodeInvalidInput)
	}
}

// The kiosk generates the id, so a request whose answer was lost comes back
// unchanged. It must find its own transaction and return it, not charge again
// (NFR-03, TC-08).
func TestCreateRepeatedReturnsTheTransactionItAlreadyMade(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	made := model.Transaction{
		ID: txID, MachineID: vm.ID, SlotID: 55, SlotNumber: 1, Status: model.StatusPending,
		Price: 25000, CreatedAt: t0.Add(-20 * time.Second),
		Product: model.Product{ID: 7, Name: "Gacha", Price: 25000},
	}
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(made, nil)

	got, err := f.uc.Create(context.Background(), vm, input())
	if err != nil {
		t.Fatal(err)
	}
	if got.Transaction.ID != txID {
		t.Errorf("transaction = %+v", got.Transaction)
	}
	// 20 of the 60 seconds are gone, and the clock is not restarted.
	if got.ExpiresIn != 40*time.Second {
		t.Errorf("expiresIn = %v, want 40s", got.ExpiresIn)
	}
}

// A transaction that is no longer pending has no time left on it, however
// recently it was made.
func TestCreateRepeatedOnAClosedTransactionOffersNoTimeLeft(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, SlotNumber: 1, Status: model.StatusDispensed, CreatedAt: t0,
	}, nil)

	got, err := f.uc.Create(context.Background(), vm, input())
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresIn != 0 {
		t.Errorf("expiresIn = %v, want 0", got.ExpiresIn)
	}
}

// One id reused for a different slot is a client bug, not a retry, and is
// refused rather than quietly answered with the wrong product.
func TestCreateRefusesAnIDReusedForAnotherSlot(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, SlotNumber: 4, Status: model.StatusPending, CreatedAt: t0,
	}, nil)

	_, err := f.uc.Create(context.Background(), vm, input())
	if got := code(t, err); got != model.CodeConflict {
		t.Errorf("code = %s, want %s", got, model.CodeConflict)
	}
}

// Another machine's transaction is reported as missing, so one agent learns
// nothing about another's purchases.
func TestCreateHidesAnotherMachinesTransaction(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: 404, SlotNumber: 1, Status: model.StatusPending,
	}, nil)

	_, err := f.uc.Create(context.Background(), vm, input())
	if got := code(t, err); got != model.CodeNotFound {
		t.Errorf("code = %s, want %s", got, model.CodeNotFound)
	}
}

// Two retries of one request can reach the insert together: the first wins and
// the second is told the id exists. The loser re-runs and finds the winner's
// transaction, so the customer still gets one answer and one charge.
func TestCreateRetriesOnceWhenTheSameIDWasInsertedConcurrently(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	winner := model.Transaction{
		ID: txID, MachineID: vm.ID, SlotID: 55, SlotNumber: 1, Status: model.StatusPending,
		Price: 25000, CreatedAt: t0, Product: model.Product{ID: 7, Price: 25000},
	}
	// First attempt: not there yet, so it tries to insert and loses the race.
	first := f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(12), nil)
	f.slots.EXPECT().LockForUpdate(gomock.Any(), vm.ID, 1).Return(stocked(5), nil)
	f.transactions.EXPECT().ExpireStale(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	f.transactions.EXPECT().CountOpenShared(gomock.Any(), gomock.Any()).Return(0, nil)
	f.transactions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(model.ErrDuplicateID)
	// Second attempt: the winner is there now.
	f.transactions.EXPECT().Get(gomock.Any(), txID).After(first).Return(winner, nil)

	got, err := f.uc.Create(context.Background(), vm, input())
	if err != nil {
		t.Fatal(err)
	}
	if got.Transaction.ID != txID {
		t.Errorf("transaction = %+v", got.Transaction)
	}
	if f.unit.Attempts != 2 {
		t.Errorf("opened the unit of work %d times, want 2", f.unit.Attempts)
	}
}

// A unit of work may be retried after a rollback, so what the closure reports
// must be built fresh each time. If the stale flag survived an attempt, this
// purchase would be refused as stale although it succeeded.
func TestCreateCarriesNothingOverFromARetriedAttempt(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// Attempt one sets the stale flag; attempt two succeeds.
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord).Times(2)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(13), nil)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(active(12), nil)
	f.slots.EXPECT().LockForUpdate(gomock.Any(), vm.ID, 1).Return(stocked(5), nil)
	f.transactions.EXPECT().ExpireStale(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	f.transactions.EXPECT().CountOpenShared(gomock.Any(), gomock.Any()).Return(0, nil)
	f.transactions.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	f.unit.RetryFirst = true
	got, err := f.uc.Create(context.Background(), vm, input())
	if err != nil {
		t.Fatalf("err = %v, want the second attempt to stand", err)
	}
	if got.Transaction.ID != txID {
		t.Errorf("transaction = %+v", got.Transaction)
	}
}

// ---------------------------------------------------------------- pay

func TestPayMovesAPendingPurchaseToPaid(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusPending, CreatedAt: t0.Add(-10 * time.Second),
	}, nil)
	f.transactions.EXPECT().MarkPaid(gomock.Any(), txID, t0).Return(nil)

	got, err := f.uc.Pay(context.Background(), vm, txID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusPaid || got.PaidAt == nil || !got.PaidAt.Equal(t0) {
		t.Errorf("transaction = %+v", got)
	}
}

// Past the window the purchase is lost, and it is stamped as having ended when
// the window closed, not when the late payment arrived: when a customer lost
// their chance is a fact about the purchase, not about the request (BR-06, TC-05).
func TestPayAfterTheWindowExpiresItAtTheWindowsEnd(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	created := t0.Add(-90 * time.Second)
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusPending, CreatedAt: created,
	}, nil)
	f.transactions.EXPECT().Finish(gomock.Any(), txID, model.StatusExpired, created.Add(window)).Return(nil)

	_, err := f.uc.Pay(context.Background(), vm, txID)
	if got := code(t, err); got != model.CodeExpired {
		t.Errorf("code = %s, want %s", got, model.CodeExpired)
	}
}

func TestPayOnAnAlreadyExpiredPurchaseChangesNothing(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusExpired, CreatedAt: t0,
	}, nil)

	_, err := f.uc.Pay(context.Background(), vm, txID)
	if got := code(t, err); got != model.CodeExpired {
		t.Errorf("code = %s, want %s", got, model.CodeExpired)
	}
}

func TestPayOnACancelledPurchaseIsRefused(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusCancelled, CreatedAt: t0,
	}, nil)

	_, err := f.uc.Pay(context.Background(), vm, txID)
	if got := code(t, err); got != model.CodeTransactionClosed {
		t.Errorf("code = %s, want %s", got, model.CodeTransactionClosed)
	}
}

// Paying one already paid returns it unchanged, so an agent that restarted
// between the payment and its answer can simply ask again (TC-08, TC-37).
func TestPayTwiceWritesNothingTheSecondTime(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	paid := t0.Add(-5 * time.Second)
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusPaid, CreatedAt: t0.Add(-10 * time.Second), PaidAt: &paid,
	}, nil)
	// No MarkPaid is expected.

	got, err := f.uc.Pay(context.Background(), vm, txID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusPaid || !got.PaidAt.Equal(paid) {
		t.Errorf("transaction = %+v", got)
	}
}

// ---------------------------------------------------------------- result

func paid() model.Transaction {
	at := t0.Add(-5 * time.Second)
	return model.Transaction{
		ID: txID, MachineID: vm.ID, SlotID: 55, SlotNumber: 1, Status: model.StatusPaid,
		Price: 25000, CreatedAt: t0.Add(-20 * time.Second), PaidAt: &at,
	}
}

// The status change and the count both happen in the one unit of work, so the
// figure an operator sees can never disagree with the sale it came from (FR-07).
func TestResultDispensedTakesTheItemOffTheSlot(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(paid(), nil)
	finished := f.transactions.EXPECT().Finish(gomock.Any(), txID, model.StatusDispensed, t0).Return(nil)
	f.slots.EXPECT().TakeOne(gomock.Any(), int64(55)).After(finished).Return(true, nil)

	got, err := f.uc.Result(context.Background(), vm, txID, purchase.ResultInput{Status: "DISPENSED"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusDispensed || got.FinishedAt == nil || !got.FinishedAt.Equal(t0) {
		t.Errorf("transaction = %+v", got)
	}
}

// The item left the machine, so the sale stands even when the server's count
// had already reached zero. Repairing the count is the next visit's work, not
// this request's (BR-07).
func TestResultDispensedStandsWhenTheCountWasAlreadyZero(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(paid(), nil)
	f.transactions.EXPECT().Finish(gomock.Any(), txID, model.StatusDispensed, t0).Return(nil)
	f.slots.EXPECT().TakeOne(gomock.Any(), int64(55)).Return(false, nil)

	got, err := f.uc.Result(context.Background(), vm, txID, purchase.ResultInput{Status: "DISPENSED"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusDispensed {
		t.Errorf("status = %s, want DISPENSED", got.Status)
	}
}

// A failed dispense leaves the count alone, raises the catalog version so every
// kiosk reloads, and blocks the slot. Machine before slot: the same lock order
// as a purchase, or the two deadlock (BR-07, TC-06).
func TestResultFailedBumpsTheVersionBeforeBlockingTheSlot(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(paid(), nil)
	failed := f.transactions.EXPECT().Fail(gomock.Any(), txID, "MOTOR_TIMEOUT", t0).Return(nil)
	bumped := f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).After(failed).Return(nil)
	f.slots.EXPECT().MarkNeedsCheck(gomock.Any(), int64(55)).After(bumped).Return(nil)
	// No TakeOne: a product that never came out was never sold.

	got, err := f.uc.Result(context.Background(), vm, txID,
		purchase.ResultInput{Status: "failed", FailureReason: "motor_timeout"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusFailed || got.FailureReason == nil || *got.FailureReason != "MOTOR_TIMEOUT" {
		t.Errorf("transaction = %+v", got)
	}
}

func TestResultRefusesWhatCannotBeReported(t *testing.T) {
	cases := []struct {
		name   string
		status string
		in     purchase.ResultInput
		want   string
	}{
		{"not yet paid", model.StatusPending, purchase.ResultInput{Status: "DISPENSED"}, model.CodeNotPaid},
		{"already cancelled", model.StatusCancelled, purchase.ResultInput{Status: "DISPENSED"}, model.CodeTransactionClosed},
		{"already expired", model.StatusExpired, purchase.ResultInput{Status: "DISPENSED"}, model.CodeTransactionClosed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			tx := paid()
			tx.Status = tc.status
			f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(tx, nil)

			_, err := f.uc.Result(context.Background(), vm, txID, tc.in)
			if got := code(t, err); got != tc.want {
				t.Errorf("code = %s, want %s", got, tc.want)
			}
		})
	}
}

// A repeat of the same result changes nothing; a different one is refused and
// answered with the result that stands, because a final state is final (BR-11).
func TestResultRepeatedIsIdempotentAndContradictionIsRefused(t *testing.T) {
	t.Run("the same result again", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		tx := paid()
		tx.Status, tx.FinishedAt = model.StatusDispensed, &t0
		f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(tx, nil)
		// Nothing is written.

		got, err := f.uc.Result(context.Background(), vm, txID, purchase.ResultInput{Status: "DISPENSED"})
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != model.StatusDispensed {
			t.Errorf("status = %s", got.Status)
		}
	})
	t.Run("a different result", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		tx := paid()
		tx.Status, tx.FinishedAt = model.StatusDispensed, &t0
		f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(tx, nil)

		_, err := f.uc.Result(context.Background(), vm, txID,
			purchase.ResultInput{Status: "FAILED", FailureReason: "MOTOR_TIMEOUT"})
		if got := code(t, err); got != model.CodeConflict {
			t.Fatalf("code = %s, want %s", got, model.CodeConflict)
		}
		// The refusal carries the state that stands, so the agent can stop asking.
		e, _ := model.AsError(err)
		if cur, ok := e.Extra["current"].(model.Transaction); !ok || cur.Status != model.StatusDispensed {
			t.Errorf("extra current = %#v", e.Extra["current"])
		}
	})
}

// The reason set is open on purpose, so a new kind of hardware fault needs no
// server change; the shape is still checked, or a FAILED row could carry
// nothing an operator can read.
func TestResultRefusesAnUnusableReport(t *testing.T) {
	cases := []struct {
		name string
		in   purchase.ResultInput
	}{
		{"a status that is neither", purchase.ResultInput{Status: "MUNGKIN"}},
		{"no status at all", purchase.ResultInput{}},
		{"failed without a reason", purchase.ResultInput{Status: "FAILED"}},
		{"failed with an unusable reason", purchase.ResultInput{Status: "FAILED", FailureReason: "???"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			// Nothing is read: the report is refused before the transaction is
			// even looked up.
			_, err := f.uc.Result(context.Background(), vm, txID, tc.in)
			if got := code(t, err); got != model.CodeInvalidInput {
				t.Errorf("code = %s, want %s", got, model.CodeInvalidInput)
			}
		})
	}
}

// ---------------------------------------------------------------- cancel

// "Batal" and the kiosk's own timer are told apart, so a recap can distinguish
// a customer who changed their mind from one who walked away (FR-11).
func TestCancelRecordsWhyThePurchaseEnded(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"CANCEL", model.StatusCancelled},
		{"", model.StatusCancelled},
		{"TIMEOUT", model.StatusExpired},
		{"timeout", model.StatusExpired},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
				ID: txID, MachineID: vm.ID, Status: model.StatusPending, CreatedAt: t0,
			}, nil)
			f.transactions.EXPECT().Finish(gomock.Any(), txID, tc.want, t0).Return(nil)

			got, err := f.uc.Cancel(context.Background(), vm, txID, tc.reason)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want || got.FinishedAt == nil {
				t.Errorf("transaction = %+v", got)
			}
		})
	}
}

// Cancelling something already finished reports it as it stands rather than
// rewriting a final state (BR-11, TC-03).
func TestCancelLeavesAFinishedPurchaseAlone(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusDispensed, CreatedAt: t0, FinishedAt: &t0,
	}, nil)
	// No Finish is expected.

	got, err := f.uc.Cancel(context.Background(), vm, txID, "CANCEL")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusDispensed {
		t.Errorf("status = %s, want DISPENSED", got.Status)
	}
}

// ---------------------------------------------------------------- report

// The sale keeps its status and still counts as revenue; the report only flags
// it for an admin to look at (FR-09, BR-03, TC-12).
func TestReportFlagsADispensedSaleWithoutChangingIt(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	finished := t0.Add(-30 * time.Second)
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusDispensed, CreatedAt: t0.Add(-time.Minute), FinishedAt: &finished,
	}, nil)
	f.transactions.EXPECT().MarkReported(gomock.Any(), txID, t0).Return(nil)

	got, err := f.uc.Report(context.Background(), vm, txID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusDispensed {
		t.Errorf("status = %s, want it unchanged", got.Status)
	}
	if got.CustomerReportedAt == nil || !got.CustomerReportedAt.Equal(t0) {
		t.Errorf("reportedAt = %v, want %v", got.CustomerReportedAt, t0)
	}
}

func TestReportRefusesWhatWasNeverDispensed(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusPending, CreatedAt: t0,
	}, nil)

	_, err := f.uc.Report(context.Background(), vm, txID)
	if got := code(t, err); got != model.CodeNotReportable {
		t.Errorf("code = %s, want %s", got, model.CodeNotReportable)
	}
}

// Long after the fact nobody can tell what happened at the machine, so the
// window closes rather than collecting reports nobody can act on.
func TestReportRefusesOneSentTooLate(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	finished := t0.Add(-10 * time.Minute)
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusDispensed, CreatedAt: finished, FinishedAt: &finished,
	}, nil)

	_, err := f.uc.Report(context.Background(), vm, txID)
	if got := code(t, err); got != model.CodeReportWindowShut {
		t.Errorf("code = %s, want %s", got, model.CodeReportWindowShut)
	}
}

// Reporting twice is the first report's answer again, even once the window has
// closed: the customer already told us.
func TestReportTwiceKeepsTheFirstReportsTime(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	long := t0.Add(-time.Hour)
	first := t0.Add(-59 * time.Minute)
	f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusDispensed, CreatedAt: long,
		FinishedAt: &long, CustomerReportedAt: &first,
	}, nil)
	// No MarkReported is expected.

	got, err := f.uc.Report(context.Background(), vm, txID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CustomerReportedAt == nil || !got.CustomerReportedAt.Equal(first) {
		t.Errorf("reportedAt = %v, want the first report's %v", got.CustomerReportedAt, first)
	}
}

// ---------------------------------------------------------------- status

// The status read changes nothing, so it needs no transaction of its own and
// goes straight to the pool-bound repository.
func TestStatusReadsWithoutOpeningAUnitOfWork(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{
		ID: txID, MachineID: vm.ID, Status: model.StatusPaid,
	}, nil)

	got, err := f.uc.Status(context.Background(), vm, txID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusPaid {
		t.Errorf("status = %s", got.Status)
	}
	if f.unit.Attempts != 0 {
		t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
	}
}

// Every step hides a transaction that is missing or belongs elsewhere behind the
// same answer.
func TestEveryStepHidesWhatIsNotThisMachinesPurchase(t *testing.T) {
	steps := map[string]func(*fixture) error{
		"status": func(f *fixture) error {
			_, err := f.uc.Status(context.Background(), vm, txID)
			return err
		},
		"pay": func(f *fixture) error {
			_, err := f.uc.Pay(context.Background(), vm, txID)
			return err
		},
		"result": func(f *fixture) error {
			_, err := f.uc.Result(context.Background(), vm, txID, purchase.ResultInput{Status: "DISPENSED"})
			return err
		},
		"cancel": func(f *fixture) error {
			_, err := f.uc.Cancel(context.Background(), vm, txID, "CANCEL")
			return err
		},
		"report": func(f *fixture) error {
			_, err := f.uc.Report(context.Background(), vm, txID)
			return err
		},
	}
	for name, step := range steps {
		for _, tc := range []struct {
			why string
			tx  model.Transaction
			err error
		}{
			{"no such transaction", model.Transaction{}, model.ErrNoRecord},
			{"another machine's", model.Transaction{ID: txID, MachineID: 404, Status: model.StatusPaid}, nil},
		} {
			t.Run(name+": "+tc.why, func(t *testing.T) {
				f, ctrl := newFixture(t)
				defer ctrl.Finish()
				f.transactions.EXPECT().Get(gomock.Any(), txID).Return(tc.tx, tc.err).AnyTimes()
				f.transactions.EXPECT().LockForUpdate(gomock.Any(), txID).Return(tc.tx, tc.err).AnyTimes()

				if got := code(t, step(f)); got != model.CodeNotFound {
					t.Errorf("code = %s, want %s", got, model.CodeNotFound)
				}
			})
		}
	}
}

// A repository failure is reported as it is, so a storage outage does not
// surface as a business refusal the kiosk would show to a customer.
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	boom := errors.New("the transactions table is unreachable")
	f.transactions.EXPECT().Get(gomock.Any(), txID).Return(model.Transaction{}, model.ErrNoRecord)
	f.machines.EXPECT().LockShared(gomock.Any(), vm.ID).Return(model.MachineRecord{}, boom)

	if _, err := f.uc.Create(context.Background(), vm, input()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}
