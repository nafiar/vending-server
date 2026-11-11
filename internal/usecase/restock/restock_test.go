package restock_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/model"
	machinemock "vending/internal/repository/machine/mock"
	restockmock "vending/internal/repository/restock/mock"
	slotmock "vending/internal/repository/slot/mock"
	"vending/internal/repository/uow/uowtest"
	"vending/internal/usecase/restock"
)

// A visit is all-or-nothing (TC-14 to TC-16), and the operator is standing at
// the machine with the door open. So the things worth testing here are not the
// happy path but the refusals: that a count that cannot be right stops the
// whole visit before anything is written, that every bad slot is named at once
// rather than one per attempt, and that a visit sent twice is saved once.

var (
	t0      = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	started = t0.Add(-4 * time.Minute)
	vm      = model.Machine{ID: 9, Code: "VM-01", Name: "Depan"}
	op      = model.Principal{UserID: 4, Name: "Budi", Username: "budi", MachineID: 9, StartedAt: started}
	visitID = "3f2a1b4c-5d6e-4f70-8a9b-0c1d2e3f4a5b"
)

type fixture struct {
	unit     *uowtest.Bundle
	machines *machinemock.MockRepository
	slots    *slotmock.MockRepository
	restocks *restockmock.MockRepository
	uc       *restock.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		machines: machinemock.NewMockRepository(ctrl),
		slots:    slotmock.NewMockRepository(ctrl),
		restocks: restockmock.NewMockRepository(ctrl),
	}
	f.unit = &uowtest.Bundle{MachineRepo: f.machines, SlotRepo: f.slots, RestockRepo: f.restocks}
	f.uc = restock.New(f.unit, f.slots, clock.New(func() time.Time { return t0 }), nil)
	return f, ctrl
}

func product(id int64, name string) *model.Product {
	return &model.Product{ID: id, Name: name, Price: 10000, IsActive: true}
}

// slotAt is a slot of the machine: capacity 10, holding product 7.
func slotAt(n int, qty int) model.Slot {
	return model.Slot{ID: int64(100 + n), MachineID: vm.ID, SlotNumber: n, Capacity: 10, Qty: qty,
		Product: product(7, "Gacha")}
}

func items(pairs ...int) []restock.ItemInput {
	var out []restock.ItemInput
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, restock.ItemInput{
			Label: strconv.Itoa(pairs[i]), SlotNumber: pairs[i], QtyAfter: pairs[i+1],
		})
	}
	return out
}

func input(its ...restock.ItemInput) restock.SaveInput {
	return restock.SaveInput{ID: visitID, Items: its}
}

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

func fields(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Fields
}

// ---------------------------------------------------------------- the screen

// Showing the screen changes nothing, so it must not open a unit of work.
func TestSlotsReadsWithoutAUnitOfWork(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.slots.EXPECT().ListByMachine(gomock.Any(), vm.ID).Return([]model.Slot{slotAt(1, 5)}, nil)

	got, err := f.uc.Slots(context.Background(), vm)
	if err != nil {
		t.Fatal(err)
	}
	if got.Machine != vm || len(got.Slots) != 1 || got.Slots[0].Capacity != 10 {
		t.Errorf("slots = %+v", got)
	}
	if f.unit.Attempts != 0 {
		t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
	}
}

// ---------------------------------------------------------------- saving

// The lock order is machine, then slots, the same as a purchase, or the two
// deadlock. Taking the machine exclusively first is also what makes the
// resend check conclusive: two sends of one visit cannot both pass it.
func TestSaveLocksTheMachineThenChecksTheResendThenLocksTheSlots(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	lockedMachine := f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).
		Return(model.MachineRecord{ID: vm.ID, CatalogVersion: 12, IsActive: true}, nil)
	checked := f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).
		After(lockedMachine).Return(model.RestockSession{}, model.ErrNoRecord)
	lockedSlots := f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).
		After(checked).Return([]model.Slot{slotAt(1, 2)}, nil)
	created := f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).After(lockedSlots).
		DoAndReturn(func(_ context.Context, s model.RestockSession) (int64, error) {
			if s.ClientID != visitID || s.MachineID != vm.ID || s.UserID != op.UserID {
				t.Errorf("session = %+v", s)
			}
			// The visit began when the operator signed in, so the record says
			// how long it took, not only when it was filed.
			if !s.StartedAt.Equal(started) || !s.FinishedAt.Equal(t0) {
				t.Errorf("session times = %+v", s)
			}
			return 55, nil
		})
	f.slots.EXPECT().Restock(gomock.Any(), int64(101), ptr(int64(7)), 8).After(created).Return(nil)
	f.restocks.EXPECT().AddItems(gomock.Any(), int64(55), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ int64, its []model.RestockItem) error {
			if len(its) != 1 {
				t.Fatalf("want one line, got %d", len(its))
			}
			it := its[0]
			if it.SlotID != 101 || it.QtyBefore != 2 || it.QtyAfter != 8 || it.ProductChanged {
				t.Errorf("line = %+v", it)
			}
			return nil
		})
	// Every kiosk must reload: what it shows was true before the door opened.
	f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

	got, err := f.uc.Save(context.Background(), vm, op, input(items(1, 8)...))
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != 55 || got.ClientID != visitID || got.CatalogVersion != 13 {
		t.Errorf("saved = %+v", got)
	}
}

// A line is written for every slot of the machine, not only the ones the
// operator typed in: the record is the evidence behind every count, so a slot
// nobody touched still has its count witnessed by the visit.
func TestSaveRecordsALineForEverySlot(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID, CatalogVersion: 1}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).
		Return([]model.Slot{slotAt(1, 2), slotAt(2, 6), slotAt(3, 0)}, nil)
	f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(55), nil)
	// Slot 1 was typed in; slots 2 and 3 keep what they had.
	f.slots.EXPECT().Restock(gomock.Any(), int64(101), ptr(int64(7)), 9).Return(nil)
	f.slots.EXPECT().Restock(gomock.Any(), int64(102), ptr(int64(7)), 6).Return(nil)
	f.slots.EXPECT().Restock(gomock.Any(), int64(103), ptr(int64(7)), 0).Return(nil)
	f.restocks.EXPECT().AddItems(gomock.Any(), int64(55), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ int64, its []model.RestockItem) error {
			if len(its) != 3 {
				t.Errorf("want a line per slot (3), got %d", len(its))
			}
			return nil
		})
	f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

	if _, err := f.uc.Save(context.Background(), vm, op, input(items(1, 9)...)); err != nil {
		t.Fatal(err)
	}
}

// A planned product change is applied by the visit, and the line records it, so
// a count that rose without anything being added is explainable (FR-16, TC-17).
func TestSaveAppliesAPlannedProductChange(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	sl := slotAt(1, 5)
	sl.NextProduct = product(8, "Gacha B")
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{sl}, nil)
	f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(55), nil)
	// The new product, and the count the operator typed.
	f.slots.EXPECT().Restock(gomock.Any(), int64(101), ptr(int64(8)), 8).Return(nil)
	f.restocks.EXPECT().AddItems(gomock.Any(), int64(55), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ int64, its []model.RestockItem) error {
			it := its[0]
			if !it.ProductChanged || it.ProductID == nil || *it.ProductID != 8 {
				t.Errorf("line = %+v, want the change recorded", it)
			}
			if it.QtyBefore != 5 || it.QtyAfter != 8 {
				t.Errorf("line = %+v", it)
			}
			return nil
		})
	f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

	if _, err := f.uc.Save(context.Background(), vm, op, input(items(1, 8)...)); err != nil {
		t.Fatal(err)
	}
}

// A product changed under a slot the operator did not type a count for: how
// many of the new product are in there is not something anyone knows, so 0 is
// the only honest answer, and the next visit corrects it.
func TestSaveZeroesASwappedSlotNobodyCounted(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	sl := slotAt(1, 5)
	sl.NextProduct = product(8, "Gacha B")
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{sl}, nil)
	f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(55), nil)
	f.slots.EXPECT().Restock(gomock.Any(), int64(101), ptr(int64(8)), 0).Return(nil)
	f.restocks.EXPECT().AddItems(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

	if _, err := f.uc.Save(context.Background(), vm, op, input()); err != nil {
		t.Fatal(err)
	}
}

// A count that cannot be right stops the whole visit, and nothing is written:
// half a visit recorded is worse than none, because it leaves the system
// confident about figures nobody checked (TC-14 to TC-16).
func TestSaveRefusesImpossibleCountsBeforeTakingAnyLock(t *testing.T) {
	cases := []struct {
		name  string
		items []restock.ItemInput
		field string
		msg   string
	}{
		{"not a slot number at all", []restock.ItemInput{{Label: "abc", SlotNumber: 0, QtyAfter: 3}},
			"slotabc", "nomor slot tidak valid"},
		{"slot below the first", items(0, 3), "slot0", "nomor slot tidak valid"},
		{"slot past the last", items(61, 3), "slot61", "nomor slot tidak valid"},
		{"a count that is not a whole number", []restock.ItemInput{{Label: "1", SlotNumber: 1, QtyAfter: -1}},
			"slot1", "jumlah harus bilangan bulat 0 sampai kapasitas"},
		{"a negative count", items(1, -1), "slot1", "jumlah harus bilangan bulat 0 sampai kapasitas"},
		{"a count past the column", items(1, 256), "slot1", "jumlah harus bilangan bulat 0 sampai kapasitas"},
		{"one slot sent twice", items(1, 3, 1, 4), "slot1", "slot dikirim dua kali"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			// No repository call at all is expected: an obviously bad request
			// must not take a lock on a machine that is busy selling.

			_, err := f.uc.Save(context.Background(), vm, op, input(tc.items...))
			if got := code(t, err); got != model.CodeInvalidInput {
				t.Fatalf("code = %s, want %s", got, model.CodeInvalidInput)
			}
			if got := fields(t, err)[tc.field]; got != tc.msg {
				t.Errorf("fields[%s] = %q, want %q", tc.field, got, tc.msg)
			}
			if f.unit.Attempts != 0 {
				t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
			}
		})
	}
}

// Every bad slot is named at once. The operator is at the machine and should be
// able to fix the lot in one go, not find the next problem after each fix.
func TestSaveNamesEveryBadSlotAtOnce(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	_, err := f.uc.Save(context.Background(), vm, op, input(items(1, -1, 61, 3, 2, 999)...))
	got := fields(t, err)
	if len(got) != 3 {
		t.Fatalf("fields = %v, want all three slots named", got)
	}
	for _, key := range []string{"slot1", "slot61", "slot2"} {
		if got[key] == "" {
			t.Errorf("%s is not named in %v", key, got)
		}
	}
}

// What a particular slot can hold needs the machine, so it is checked after the
// slots are read — and still before anything is written.
func TestSaveRefusesCountsTheMachineCannotHold(t *testing.T) {
	empty := slotAt(2, 0)
	empty.Product = nil
	cases := []struct {
		name  string
		slots []model.Slot
		items []restock.ItemInput
		field string
		msg   string
	}{
		{"more than the slot holds", []model.Slot{slotAt(1, 0)}, items(1, 11), "slot1", "maksimal 10"},
		{"a count for a slot with no product", []model.Slot{empty}, items(2, 1), "slot2",
			"slot belum punya produk, isi harus 0"},
		{"a slot this machine does not have", []model.Slot{slotAt(1, 0)}, items(7, 1), "slot7",
			"slot tidak ada di mesin ini"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
			f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
			f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return(tc.slots, nil)
			// Nothing is created and no slot is written.

			_, err := f.uc.Save(context.Background(), vm, op, input(tc.items...))
			if got := code(t, err); got != model.CodeInvalidInput {
				t.Fatalf("code = %s, want %s", got, model.CodeInvalidInput)
			}
			if got := fields(t, err)[tc.field]; got != tc.msg {
				t.Errorf("fields[%s] = %q, want %q", tc.field, got, tc.msg)
			}
		})
	}
}

// Zero is always allowed, even for a slot with no product: emptying a slot is
// the normal way to report one.
func TestSaveAllowsZeroForAnEmptySlot(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	empty := slotAt(1, 0)
	empty.Product = nil
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{empty}, nil)
	f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(55), nil)
	f.slots.EXPECT().Restock(gomock.Any(), int64(101), nil, 0).Return(nil)
	f.restocks.EXPECT().AddItems(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

	if _, err := f.uc.Save(context.Background(), vm, op, input(items(1, 0)...)); err != nil {
		t.Fatal(err)
	}
}

// Exactly the capacity is allowed: it is a full slot, not an impossible one.
func TestSaveAllowsExactlyTheCapacity(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{slotAt(1, 0)}, nil)
	f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(55), nil)
	f.slots.EXPECT().Restock(gomock.Any(), int64(101), ptr(int64(7)), 10).Return(nil)
	f.restocks.EXPECT().AddItems(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

	if _, err := f.uc.Save(context.Background(), vm, op, input(items(1, 10)...)); err != nil {
		t.Fatal(err)
	}
}

// A resend returns the first visit's answer and writes nothing: a visit saved
// twice would double-count what was loaded (TC-13).
func TestSaveResentReturnsTheFirstVisit(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	first := t0.Add(-time.Minute)
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).
		Return(model.MachineRecord{ID: vm.ID, CatalogVersion: 13}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{
		ID: 55, ClientID: visitID, MachineID: vm.ID, FinishedAt: first,
	}, nil)
	// No slot is locked, nothing is created, the version is not raised again.

	got, err := f.uc.Save(context.Background(), vm, op, input(items(1, 8)...))
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != 55 || !got.SavedAt.Equal(first) {
		t.Errorf("saved = %+v, want the first visit's", got)
	}
	// The version is the one that already includes the first save.
	if got.CatalogVersion != 13 {
		t.Errorf("catalogVersion = %d, want 13", got.CatalogVersion)
	}
}

// One visit id used by another machine is a client mistake, not a resend, and
// is refused rather than answered with someone else's visit.
func TestSaveRefusesAVisitIDUsedByAnotherMachine(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).
		Return(model.RestockSession{ID: 55, ClientID: visitID, MachineID: 404}, nil)

	_, err := f.uc.Save(context.Background(), vm, op, input(items(1, 8)...))
	if got := code(t, err); got != model.CodeConflict {
		t.Errorf("code = %s, want %s", got, model.CodeConflict)
	}
}

// Two sends racing past the resend check: rather than guess which won, the
// kiosk is told to ask again, and the next ask finds the winner.
func TestSaveAsksTheKioskToRetryWhenTwoSendsRace(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{slotAt(1, 2)}, nil)
	f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(0), model.ErrDuplicateID)
	// No slot is written: the visit did not happen in this request.

	_, err := f.uc.Save(context.Background(), vm, op, input(items(1, 8)...))
	if got := code(t, err); got != model.CodeConflict {
		t.Errorf("code = %s, want %s", got, model.CodeConflict)
	}
}

// The kiosk's own time is kept when it is plausible, so a visit the agent
// journaled while offline keeps the time it actually happened; anything
// impossible falls back to now.
func TestSaveKeepsAPlausibleFinishTime(t *testing.T) {
	cases := []struct {
		name string
		sent *time.Time
		want time.Time
	}{
		{"nothing sent", nil, t0},
		{"during the visit", ptrTime(t0.Add(-time.Minute)), t0.Add(-time.Minute)},
		{"exactly now", ptrTime(t0), t0},
		{"exactly when the visit began", ptrTime(started), started},
		{"before the visit began", ptrTime(started.Add(-time.Hour)), t0},
		{"in the future", ptrTime(t0.Add(time.Hour)), t0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
			f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
			f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{slotAt(1, 2)}, nil)
			f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, s model.RestockSession) (int64, error) {
					if !s.FinishedAt.Equal(tc.want) {
						t.Errorf("finishedAt = %v, want %v", s.FinishedAt, tc.want)
					}
					return 55, nil
				})
			f.slots.EXPECT().Restock(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			f.restocks.EXPECT().AddItems(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

			in := input(items(1, 8)...)
			in.FinishedAt = tc.sent
			got, err := f.uc.Save(context.Background(), vm, op, in)
			if err != nil {
				t.Fatal(err)
			}
			if !got.SavedAt.Equal(tc.want) {
				t.Errorf("savedAt = %v, want %v", got.SavedAt, tc.want)
			}
		})
	}
}

func TestSaveRefusesABadVisitIDOrAnOverlongNote(t *testing.T) {
	t.Run("not a UUID", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		in := input(items(1, 8)...)
		in.ID = "bukan-uuid"

		_, err := f.uc.Save(context.Background(), vm, op, in)
		if got := code(t, err); got != model.CodeInvalidInput {
			t.Fatalf("code = %s, want %s", got, model.CodeInvalidInput)
		}
		if fields(t, err)["id"] == "" {
			t.Error("the refusal must name the id field")
		}
	})
	t.Run("note too long", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		in := input(items(1, 8)...)
		in.Note = strings.Repeat("a", model.MaxNoteRunes+1)

		_, err := f.uc.Save(context.Background(), vm, op, in)
		if got := code(t, err); got != model.CodeInvalidInput {
			t.Fatalf("code = %s, want %s", got, model.CodeInvalidInput)
		}
		if fields(t, err)["note"] == "" {
			t.Error("the refusal must name the note field")
		}
	})
	t.Run("a note of exactly the limit is fine", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
		f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
		f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return([]model.Slot{slotAt(1, 2)}, nil)
		f.restocks.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(55), nil)
		f.slots.EXPECT().Restock(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		f.restocks.EXPECT().AddItems(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
		f.machines.EXPECT().BumpCatalogVersion(gomock.Any(), vm.ID).Return(nil)

		in := input(items(1, 8)...)
		in.Note = strings.Repeat("é", model.MaxNoteRunes) // counted in characters, not bytes
		if _, err := f.uc.Save(context.Background(), vm, op, in); err != nil {
			t.Fatal(err)
		}
	})
}

// A storage failure is reported as itself, so an outage does not look to the
// operator like a count they typed wrong.
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	boom := errors.New("the slots table is unreachable")
	f.machines.EXPECT().LockForUpdate(gomock.Any(), vm.ID).Return(model.MachineRecord{ID: vm.ID}, nil)
	f.restocks.EXPECT().ByClientID(gomock.Any(), visitID).Return(model.RestockSession{}, model.ErrNoRecord)
	f.slots.EXPECT().LockAllByMachine(gomock.Any(), vm.ID).Return(nil, boom)

	if _, err := f.uc.Save(context.Background(), vm, op, input(items(1, 8)...)); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}

func ptr(v int64) *int64             { return &v }
func ptrTime(t time.Time) *time.Time { return &t }
