package catalog_test

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
	"vending/internal/usecase/catalog"
)

// These tests are the point of the layering: BR-04 is checked without a
// database, because every boundary the usecase depends on is an interface.

var t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)

// fixture is the usecase with every boundary mocked. The unit of work comes
// from the shared test bundle, so adding a getter to uow.Tx does not break
// tests that never reach for it.
type fixture struct {
	unit         *uowtest.Bundle
	machines     *machinemock.MockRepository
	slots        *slotmock.MockRepository
	transactions *transactionmock.MockRepository
}

func newFixture(t *testing.T, window time.Duration) (*fixture, catalog.UseCase, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		machines:     machinemock.NewMockRepository(ctrl),
		slots:        slotmock.NewMockRepository(ctrl),
		transactions: transactionmock.NewMockRepository(ctrl),
	}
	f.unit = &uowtest.Bundle{MachineRepo: f.machines, SlotRepo: f.slots, TransactionRepo: f.transactions}
	uc := catalog.New(f.unit, f.machines, clock.New(func() time.Time { return t0 }), window, nil)
	return f, uc, ctrl
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
			f, uc, ctrl := newFixture(t, time.Minute)
			defer ctrl.Finish()
			f.machines.EXPECT().Get(gomock.Any(), int64(9)).
				Return(model.MachineRecord{ID: 9, Code: "VM-01", Name: "Depan", IsActive: tc.machineActive, CatalogVersion: 12}, nil)
			f.slots.EXPECT().ListByMachine(gomock.Any(), int64(9)).Return([]model.Slot{tc.slot}, nil)
			f.transactions.EXPECT().CountOpenBySlot(gomock.Any(), int64(9), gomock.Any()).
				Return(map[int64]int{tc.slot.ID: tc.open}, nil)

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
	f, uc, ctrl := newFixture(t, 90*time.Second)
	defer ctrl.Finish()
	f.machines.EXPECT().Get(gomock.Any(), gomock.Any()).Return(model.MachineRecord{ID: 9, IsActive: true}, nil)
	f.slots.EXPECT().ListByMachine(gomock.Any(), gomock.Any()).Return(nil, nil)
	f.transactions.EXPECT().CountOpenBySlot(gomock.Any(), int64(9), t0.Add(-90*time.Second)).Return(nil, nil)

	if _, err := uc.Get(context.Background(), model.Machine{ID: 9}); err != nil {
		t.Fatal(err)
	}
}

// A repository failure is reported, never swallowed into an empty catalog: a
// kiosk showing nothing looks like a sold-out machine.
func TestGetReportsARepositoryFailure(t *testing.T) {
	f, uc, ctrl := newFixture(t, time.Minute)
	defer ctrl.Finish()
	boom := errors.New("the slots table is unreachable")
	f.machines.EXPECT().Get(gomock.Any(), gomock.Any()).Return(model.MachineRecord{ID: 9, IsActive: true}, nil)
	f.slots.EXPECT().ListByMachine(gomock.Any(), gomock.Any()).Return(nil, boom)

	if _, err := uc.Get(context.Background(), model.Machine{ID: 9}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}

// Heartbeat records the contact before reporting the version (FR-28), so an
// agent that polls is never recorded as absent.
func TestHeartbeatRecordsContactThenReportsTheVersion(t *testing.T) {
	f, uc, ctrl := newFixture(t, time.Minute)
	defer ctrl.Finish()
	touched := f.machines.EXPECT().TouchSeen(gomock.Any(), int64(9), t0).Return(nil)
	f.machines.EXPECT().Get(gomock.Any(), int64(9)).After(touched).
		Return(model.MachineRecord{ID: 9, CatalogVersion: 31}, nil)

	hb, err := uc.Heartbeat(context.Background(), model.Machine{ID: 9})
	if err != nil {
		t.Fatal(err)
	}
	if hb.CatalogVersion != 31 || hb.ServerTime != t0.UnixMilli() {
		t.Errorf("heartbeat = %+v", hb)
	}
}
