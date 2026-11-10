package catalog_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/model"
	"vending/internal/repository/machine"
	machinemock "vending/internal/repository/machine/mock"
	"vending/internal/repository/slot"
	slotmock "vending/internal/repository/slot/mock"
	"vending/internal/repository/transaction"
	transactionmock "vending/internal/repository/transaction/mock"
	"vending/internal/repository/uow"
	"vending/internal/usecase/catalog"
)

// These tests are the point of the layering: BR-04 is checked without a
// database, because every boundary the usecase depends on is an interface.

var t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)

// fakeUnit is a unit of work over mock repositories. Do and Read just call fn,
// which is the behaviour a usecase may rely on: the bundle it is handed is
// bound to one unit.
type fakeUnit struct {
	machines     *machinemock.MockRepository
	slots        *slotmock.MockRepository
	transactions *transactionmock.MockRepository
}

func (f *fakeUnit) Do(ctx context.Context, fn func(uow.Tx) error) error   { return fn(f) }
func (f *fakeUnit) Read(ctx context.Context, fn func(uow.Tx) error) error { return fn(f) }

func (f *fakeUnit) Machines() machine.Repository         { return f.machines }
func (f *fakeUnit) Slots() slot.Repository               { return f.slots }
func (f *fakeUnit) Transactions() transaction.Repository { return f.transactions }

// Compile-time proof that the fake is a usable unit of work.
var _ uow.Repository = (*fakeUnit)(nil)
var _ uow.Tx = (*fakeUnit)(nil)

func newUnit(t *testing.T) (*fakeUnit, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	return &fakeUnit{
		machines:     machinemock.NewMockRepository(ctrl),
		slots:        slotmock.NewMockRepository(ctrl),
		transactions: transactionmock.NewMockRepository(ctrl),
	}, ctrl
}

func product(id int64, active bool) *model.Product {
	return &model.Product{ID: id, Name: "Gacha", Price: 25000, IsActive: active}
}

// A slot sells only when all four conditions of BR-04 hold. Each row below
// breaks exactly one of them.
func TestGetAppliesTheAvailabilityRule(t *testing.T) {
	cases := []struct {
		name          string
		machineActive bool
		slot          model.Slot
		open          int
		want          bool
	}{
		{"everything in order", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 5, Product: product(7, true)}, 0, true},
		{"machine deactivated", false, model.Slot{ID: 1, SlotNumber: 1, Qty: 5, Product: product(7, true)}, 0, false},
		{"slot empty", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 0, Product: product(7, true)}, 0, false},
		{"no product in the slot", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 5}, 0, false},
		{"product archived", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 5, Product: product(7, false)}, 0, false},
		{"slot blocked by a failed dispense", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 5, NeedsCheck: true, Product: product(7, true)}, 0, false},
		{"the last item is already claimed", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 1, Product: product(7, true)}, 1, false},
		{"one left after the claims", true, model.Slot{ID: 1, SlotNumber: 1, Qty: 2, Product: product(7, true)}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unit, ctrl := newUnit(t)
			defer ctrl.Finish()
			unit.machines.EXPECT().Get(gomock.Any(), int64(9)).
				Return(model.MachineRecord{ID: 9, Code: "VM-01", Name: "Depan", IsActive: tc.machineActive, CatalogVersion: 12}, nil)
			unit.slots.EXPECT().ListByMachine(gomock.Any(), int64(9)).Return([]model.Slot{tc.slot}, nil)
			unit.transactions.EXPECT().CountOpenBySlot(gomock.Any(), int64(9), gomock.Any()).
				Return(map[int64]int{tc.slot.ID: tc.open}, nil)

			uc := catalog.New(unit, unit.machines, clock.New(func() time.Time { return t0 }), time.Minute, nil)
			got, err := uc.Get(context.Background(), model.Machine{ID: 9})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Slots) != 1 {
				t.Fatalf("want one slot, got %d", len(got.Slots))
			}
			if got.Slots[0].Available != tc.want {
				t.Errorf("available = %v, want %v", got.Slots[0].Available, tc.want)
			}
			if got.CatalogVersion != 12 || got.Machine.Code != "VM-01" {
				t.Errorf("catalog = %+v", got)
			}
		})
	}
}

// The payment window decides which PENDING transactions still hold an item, so
// the cutoff the usecase passes down must be now minus that window (BR-06).
func TestGetCountsOpenTransactionsFromTheWindowStart(t *testing.T) {
	unit, ctrl := newUnit(t)
	defer ctrl.Finish()
	unit.machines.EXPECT().Get(gomock.Any(), gomock.Any()).Return(model.MachineRecord{ID: 9, IsActive: true}, nil)
	unit.slots.EXPECT().ListByMachine(gomock.Any(), gomock.Any()).Return(nil, nil)
	unit.transactions.EXPECT().CountOpenBySlot(gomock.Any(), int64(9), t0.Add(-90*time.Second)).Return(nil, nil)

	uc := catalog.New(unit, unit.machines, clock.New(func() time.Time { return t0 }), 90*time.Second, nil)
	if _, err := uc.Get(context.Background(), model.Machine{ID: 9}); err != nil {
		t.Fatal(err)
	}
}

// A repository failure is reported, never swallowed into an empty catalog: a
// kiosk showing nothing looks like a sold-out machine.
func TestGetReportsARepositoryFailure(t *testing.T) {
	unit, ctrl := newUnit(t)
	defer ctrl.Finish()
	boom := errors.New("the slots table is unreachable")
	unit.machines.EXPECT().Get(gomock.Any(), gomock.Any()).Return(model.MachineRecord{ID: 9, IsActive: true}, nil)
	unit.slots.EXPECT().ListByMachine(gomock.Any(), gomock.Any()).Return(nil, boom)

	uc := catalog.New(unit, unit.machines, clock.New(func() time.Time { return t0 }), time.Minute, nil)
	if _, err := uc.Get(context.Background(), model.Machine{ID: 9}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}

// Heartbeat records the contact before reporting the version (FR-28), so an
// agent that polls is never recorded as absent.
func TestHeartbeatRecordsContactThenReportsTheVersion(t *testing.T) {
	unit, ctrl := newUnit(t)
	defer ctrl.Finish()
	touched := unit.machines.EXPECT().TouchSeen(gomock.Any(), int64(9), t0).Return(nil)
	unit.machines.EXPECT().Get(gomock.Any(), int64(9)).After(touched).
		Return(model.MachineRecord{ID: 9, CatalogVersion: 31}, nil)

	uc := catalog.New(unit, unit.machines, clock.New(func() time.Time { return t0 }), time.Minute, nil)
	hb, err := uc.Heartbeat(context.Background(), model.Machine{ID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if hb.CatalogVersion != 31 || hb.ServerTime != t0.UnixMilli() {
		t.Errorf("heartbeat = %+v", hb)
	}
}
