package machine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/model"
	auditmock "vending/internal/repository/audit/mock"
	machinemock "vending/internal/repository/machine/mock"
	productmock "vending/internal/repository/product/mock"
	reportmock "vending/internal/repository/report/mock"
	sessionmock "vending/internal/repository/session/mock"
	slotmock "vending/internal/repository/slot/mock"
	"vending/internal/repository/uow/uowtest"
	usermock "vending/internal/repository/user/mock"
	machineuc "vending/internal/usecase/machine"
)

// Two things in here are easy to get wrong and expensive when they are: the
// machine key, which exists in one place for one moment and must never reach
// the change log, and slot planning, which must not take effect until somebody
// opens the machine (BR-08, TC-18).

var (
	// 22 Oct 2026 10:00 WIB. The month figure on a card is a local month, so
	// the fixture is a time where UTC and WIB fall in the same one.
	t0      = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	jakarta = func() *time.Location {
		loc, err := time.LoadLocation("Asia/Jakarta")
		if err != nil {
			panic(err)
		}
		return loc
	}()
	actor = model.Principal{UserID: 3, Name: "Admin", Username: "admin", Role: model.RoleAdmin}
)

type fixture struct {
	unit     *uowtest.Bundle
	machines *machinemock.MockRepository
	slots    *slotmock.MockRepository
	products *productmock.MockRepository
	sessions *sessionmock.MockRepository
	users    *usermock.MockRepository
	audit    *auditmock.MockRepository
	reports  *reportmock.MockRepository
	uc       *machineuc.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		machines: machinemock.NewMockRepository(ctrl),
		slots:    slotmock.NewMockRepository(ctrl),
		products: productmock.NewMockRepository(ctrl),
		sessions: sessionmock.NewMockRepository(ctrl),
		users:    usermock.NewMockRepository(ctrl),
		audit:    auditmock.NewMockRepository(ctrl),
		reports:  reportmock.NewMockRepository(ctrl),
	}
	f.unit = &uowtest.Bundle{
		MachineRepo: f.machines, SlotRepo: f.slots, ProductRepo: f.products,
		SessionRepo: f.sessions, UserRepo: f.users, AuditRepo: f.audit,
	}
	f.uc = machineuc.New(f.unit, f.machines, f.slots, f.reports,
		clock.New(func() time.Time { return t0 }), jakarta, nil)
	return f, ctrl
}

func vm() model.MachineRecord {
	return model.MachineRecord{
		ID: 9, Code: "VM-01", Name: "Depan", Location: "Lobi", IsActive: true,
		CatalogVersion: 12, CreatedAt: t0.Add(-24 * time.Hour),
	}
}

func input() machineuc.Input {
	return machineuc.Input{Code: "VM-02", Name: "Belakang", Location: "Kantin", SlotCount: 10, Capacity: 8}
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

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Fields
}

// expectGet is the read-back every write answers with: the record, the three
// aggregates, and the slots.
func (f *fixture) expectGet(m model.MachineRecord) {
	f.machines.EXPECT().List(gomock.Any(), m.ID).Return([]model.MachineRecord{m}, nil)
	f.reports.EXPECT().SlotStats(gomock.Any(), m.ID).Return(nil, nil)
	f.reports.EXPECT().MachineSales(gomock.Any(), gomock.Any(), m.ID).Return(nil, nil)
	f.reports.EXPECT().MachineLastSale(gomock.Any(), m.ID).Return(nil, nil)
	f.slots.EXPECT().ListByMachine(gomock.Any(), m.ID).Return(nil, nil)
}

// ---------------------------------------------------------------- the cards

// Each aggregate is one query for every machine at once, not one per card: the
// list is a screen, not a loop.
func TestListReadsEachAggregateOnceForAllMachines(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	other := vm()
	other.ID, other.Code = 11, "VM-02"
	lastSale := t0.Add(-2 * time.Hour)

	f.machines.EXPECT().List(gomock.Any(), int64(0)).Return([]model.MachineRecord{vm(), other}, nil)
	f.reports.EXPECT().SlotStats(gomock.Any(), int64(0)).
		Return(map[int64]model.SlotStats{9: {Count: 10, Empty: 2, Blocked: 1}}, nil)
	f.reports.EXPECT().MachineSales(gomock.Any(), gomock.Any(), int64(0)).
		Return(map[int64]model.Totals{9: {Revenue: 125000, Sales: 5}}, nil)
	f.reports.EXPECT().MachineLastSale(gomock.Any(), int64(0)).
		Return(map[int64]time.Time{9: lastSale}, nil)

	got, err := f.uc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want two machines, got %d", len(got))
	}
	if got[0].Slots.Count != 10 || got[0].Month.Revenue != 125000 {
		t.Errorf("first card = %+v", got[0])
	}
	if got[0].LastSaleAt == nil || !got[0].LastSaleAt.Equal(lastSale) {
		t.Errorf("lastSaleAt = %v", got[0].LastSaleAt)
	}
	// A machine missing from an aggregate reads as zero, not as an error.
	if got[1].Slots.Count != 0 || got[1].Month.Revenue != 0 || got[1].LastSaleAt != nil {
		t.Errorf("second card = %+v, want zeroes", got[1])
	}
	// The list carries no coils: it shows counts.
	if got[0].Coils != nil {
		t.Error("the list must not carry every machine's slots")
	}
}

// The revenue on a card is this calendar month in the server's zone, which for
// a machine in Jakarta is not the same month UTC would give at the edges.
func TestListAsksForTheLocalMonth(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// 1 Oct 2026 02:00 WIB is still 30 Sep in UTC: the month must start on
	// 1 Oct WIB, which is 30 Sep 17:00 UTC.
	now := time.Date(2026, 9, 30, 19, 0, 0, 0, time.UTC)
	uc := machineuc.New(f.unit, f.machines, f.slots, f.reports,
		clock.New(func() time.Time { return now }), jakarta, nil)

	f.machines.EXPECT().List(gomock.Any(), int64(0)).Return([]model.MachineRecord{vm()}, nil)
	f.reports.EXPECT().SlotStats(gomock.Any(), int64(0)).Return(nil, nil)
	f.reports.EXPECT().MachineSales(gomock.Any(), time.Date(2026, 9, 30, 17, 0, 0, 0, time.UTC), int64(0)).
		Return(nil, nil)
	f.reports.EXPECT().MachineLastSale(gomock.Any(), int64(0)).Return(nil, nil)

	if _, err := uc.List(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGetFillsInTheSlotsAndRefusesAnUnknownMachine(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().List(gomock.Any(), int64(9)).Return([]model.MachineRecord{vm()}, nil)
		f.reports.EXPECT().SlotStats(gomock.Any(), int64(9)).Return(nil, nil)
		f.reports.EXPECT().MachineSales(gomock.Any(), gomock.Any(), int64(9)).Return(nil, nil)
		f.reports.EXPECT().MachineLastSale(gomock.Any(), int64(9)).Return(nil, nil)
		f.slots.EXPECT().ListByMachine(gomock.Any(), int64(9)).
			Return([]model.Slot{{ID: 101, SlotNumber: 1, Capacity: 8}}, nil)

		got, err := f.uc.Get(context.Background(), 9)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Coils) != 1 {
			t.Errorf("coils = %+v, want the slots filled in", got.Coils)
		}
	})
	t.Run("not there", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().List(gomock.Any(), int64(404)).Return(nil, nil)

		_, err := f.uc.Get(context.Background(), 404)
		if got := code(t, err); got != model.CodeNotFound {
			t.Errorf("code = %s, want %s", got, model.CodeNotFound)
		}
	})
}

// ---------------------------------------------------------------- create

// The machine, its slots and the log row all commit together, and the key is
// stored only as a hash (FR-21, FR-26, TC-26).
func TestCreateRegistersTheMachineItsSlotsAndTheLogRowTogether(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	var key string

	created := f.machines.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, m model.MachineRecord, keyHash string) (int64, error) {
			if m.Code != "VM-02" || m.Name != "Belakang" || m.Location != "Kantin" {
				t.Errorf("machine = %+v", m)
			}
			if !m.CreatedAt.Equal(t0) {
				t.Errorf("createdAt = %v", m.CreatedAt)
			}
			if keyHash == "" {
				t.Error("no key hash was stored")
			}
			key = keyHash
			return 11, nil
		})
	f.slots.EXPECT().CreateMany(gomock.Any(), int64(11), 10, 8).After(created).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionCreate || e.EntityType != model.EntityMachine || e.EntityID != 11 {
				t.Errorf("entry = %+v", e)
			}
			if e.Label != "VM-02 Belakang" {
				t.Errorf("label = %q, want the code and the name", e.Label)
			}
			if e.After["slots"] != int64(10) || e.After["capacity"] != int64(8) {
				t.Errorf("after = %v", e.After)
			}
			// The key must never reach the change log: a log is read by people.
			for k, v := range e.After {
				if s, ok := v.(string); ok && strings.HasPrefix(s, "vmk_") {
					t.Errorf("after[%s] carries the machine key", k)
				}
			}
			return nil
		})
	m := vm()
	m.ID, m.Code = 11, "VM-02"
	f.expectGet(m)

	got, err := f.uc.Create(context.Background(), actor, input())
	if err != nil {
		t.Fatal(err)
	}
	// The key comes back once, in the clear, and is not what was stored.
	if !strings.HasPrefix(got.APIKey, "vmk_") || len(got.APIKey) < 20 {
		t.Errorf("apiKey = %q, want a real key", got.APIKey)
	}
	if got.APIKey == key {
		t.Error("the key was stored in the clear; only its hash may be")
	}
	if f.unit.Attempts != 1 {
		t.Errorf("opened the unit of work %d times, want 1", f.unit.Attempts)
	}
}

// A code already taken is reported on the code field, so the form marks the box
// rather than showing a banner that says nothing about where to look.
func TestCreateReportsATakenCodeOnTheCodeField(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(int64(0), model.ErrDuplicateID)
	// No slots are made and no log row is written.

	_, err := f.uc.Create(context.Background(), actor, input())
	if got := code(t, err); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
	if got := fieldsOf(t, err)["code"]; !strings.Contains(got, "VM-02") {
		t.Errorf("fields[code] = %q, want the code named", got)
	}
}

func TestCreateReportsEveryBadFieldAtOnce(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// Nothing is read: the form is refused before any lock.

	_, err := f.uc.Create(context.Background(), actor, machineuc.Input{
		Code:      "vm 1!", // lower case and a space
		Name:      "",
		Location:  strings.Repeat("x", model.MaxMachineLocationRunes+1),
		SlotCount: 0,
		Capacity:  99,
	})
	if got := code(t, err); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
	got := fieldsOf(t, err)
	for _, field := range []string{"code", "name", "location", "slotCount", "capacity"} {
		if got[field] == "" {
			t.Errorf("%s is not reported in %v", field, got)
		}
	}
	if f.unit.Attempts != 0 {
		t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
	}
}

// The boundaries are allowed: 60 slots and capacity 30 are the limits, not past
// them (TC-28).
func TestCreateAllowsTheExactLimits(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).Return(int64(11), nil)
	f.slots.EXPECT().CreateMany(gomock.Any(), int64(11), model.MaxSlots, model.MaxSlotCapacity).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	m := vm()
	m.ID = 11
	f.expectGet(m)

	in := input()
	in.SlotCount, in.Capacity = model.MaxSlots, model.MaxSlotCapacity
	if _, err := f.uc.Create(context.Background(), actor, in); err != nil {
		t.Fatal(err)
	}
}

// The code is upper-cased and trimmed, which is a rule about codes rather than
// something the table does.
func TestCreateNormalizesTheCode(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().Create(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, m model.MachineRecord, _ string) (int64, error) {
			if m.Code != "VM-02" {
				t.Errorf("code = %q, want VM-02", m.Code)
			}
			return 11, nil
		})
	f.slots.EXPECT().CreateMany(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	m := vm()
	m.ID = 11
	f.expectGet(m)

	in := input()
	in.Code = "  vm-02  "
	if _, err := f.uc.Create(context.Background(), actor, in); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- activate

// Deactivating ends the operator sessions on that kiosk in the same commit:
// whoever is signed in should not go on restocking a machine that is meant to
// be out of service (BR-12, TC-27).
func TestDeactivatingEndsTheSessionsOnThatKiosk(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().LockForUpdate(gomock.Any(), int64(9)).Return(vm(), nil)
	f.machines.EXPECT().SetActive(gomock.Any(), int64(9), false).Return(nil)
	f.sessions.EXPECT().DeleteByMachine(gomock.Any(), int64(9)).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			// Its own action, because "a machine stopped selling" is what
			// somebody will come looking for.
			if e.Action != model.ActionDeactivate {
				t.Errorf("action = %s, want %s", e.Action, model.ActionDeactivate)
			}
			return nil
		})
	off := vm()
	off.IsActive = false
	f.expectGet(off)

	if _, err := f.uc.SetActive(context.Background(), actor, 9, false); err != nil {
		t.Fatal(err)
	}
}

// Reactivating does not touch sessions: there are none to end, and the people
// who had them are not owed a new one.
func TestReactivatingLeavesSessionsAlone(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	off := vm()
	off.IsActive = false
	f.machines.EXPECT().LockForUpdate(gomock.Any(), int64(9)).Return(off, nil)
	f.machines.EXPECT().SetActive(gomock.Any(), int64(9), true).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionUpdate {
				t.Errorf("action = %s, want %s", e.Action, model.ActionUpdate)
			}
			return nil
		})
	f.expectGet(vm())
	// No DeleteByMachine is expected.

	if _, err := f.uc.SetActive(context.Background(), actor, 9, true); err != nil {
		t.Fatal(err)
	}
}

// Setting it to what it already is changes nothing, so a double click writes no
// second log row saying the same thing.
func TestSetActiveToWhatItAlreadyIsWritesNothing(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.machines.EXPECT().LockForUpdate(gomock.Any(), int64(9)).Return(vm(), nil)
	// No SetActive, no session delete, no audit Write.
	f.expectGet(vm())

	if _, err := f.uc.SetActive(context.Background(), actor, 9, true); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- the key

// The new key is returned once and only its hash is stored; the log says the
// key changed and not what it was.
func TestRotateKeyStoresOnlyTheHashAndKeepsItOutOfTheLog(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	var stored string
	f.machines.EXPECT().LockForUpdate(gomock.Any(), int64(9)).Return(vm(), nil)
	f.machines.EXPECT().SetAPIKeyHash(gomock.Any(), int64(9), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ int64, h string) error { stored = h; return nil })
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Before["apiKey"] != "(kunci lama)" || e.After["apiKey"] != "(kunci baru)" {
				t.Errorf("entry = %v -> %v", e.Before, e.After)
			}
			return nil
		})
	f.expectGet(vm())

	got, err := f.uc.RotateKey(context.Background(), actor, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.APIKey, "vmk_") {
		t.Errorf("apiKey = %q", got.APIKey)
	}
	if got.APIKey == stored {
		t.Error("the key was stored in the clear; only its hash may be")
	}
}

// On the command line nobody is signed in, so the change is attributed to the
// first active admin. With no admin at all it is refused rather than logged
// against nobody: an unattributable row is worse than no rotation (FR-26).
func TestRotateKeyByCodeNeedsSomebodyToAttributeItTo(t *testing.T) {
	t.Run("an admin exists", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().ByCode(gomock.Any(), "VM-01").Return(vm(), nil)
		f.users.EXPECT().FirstActiveAdminID(gomock.Any()).Return(int64(3), nil)
		f.machines.EXPECT().LockForUpdate(gomock.Any(), int64(9)).Return(vm(), nil)
		f.machines.EXPECT().SetAPIKeyHash(gomock.Any(), int64(9), gomock.Any()).Return(nil)
		f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
				if e.ActorID != 3 {
					t.Errorf("actorID = %d, want the first admin", e.ActorID)
				}
				return nil
			})
		f.expectGet(vm())

		if _, err := f.uc.RotateKeyByCode(context.Background(), " vm-01 "); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no admin exists", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().ByCode(gomock.Any(), "VM-01").Return(vm(), nil)
		f.users.EXPECT().FirstActiveAdminID(gomock.Any()).Return(int64(0), model.ErrNoRecord)
		// The key is not rotated.

		if _, err := f.uc.RotateKeyByCode(context.Background(), "VM-01"); err == nil {
			t.Fatal("want a refusal")
		}
	})
	t.Run("no such machine", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.machines.EXPECT().ByCode(gomock.Any(), "VM-99").Return(model.MachineRecord{}, model.ErrNoRecord)

		_, err := f.uc.RotateKeyByCode(context.Background(), "VM-99")
		if got := code(t, err); got != model.CodeNotFound {
			t.Errorf("code = %s, want %s", got, model.CodeNotFound)
		}
	})
}

// ---------------------------------------------------------------- slots

func slotAt(qty int, held, next *model.Product) model.Slot {
	return model.Slot{
		ID: 101, MachineID: 9, SlotNumber: 1, Capacity: 8, Qty: qty,
		Product: held, NextProduct: next,
	}
}

func active(id int64, name string) *model.Product {
	return &model.Product{ID: id, Name: name, Price: 10000, IsActive: true}
}

// A plan is recorded and nothing else happens: the kiosk goes on selling what
// is in the coil, so the catalog version is deliberately not raised (BR-08,
// TC-18).
func TestUpdateSlotRecordsAPlanWithoutDisturbingTheKiosk(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 1).Return(slotAt(5, active(7, "Gacha A"), nil), nil)
	f.machines.EXPECT().Get(gomock.Any(), int64(9)).Return(vm(), nil)
	f.products.EXPECT().LockShared(gomock.Any(), int64(8)).Return(*active(8, "Gacha B"), nil)
	f.slots.EXPECT().SetPlan(gomock.Any(), int64(101), 8, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ int64, _ int, next *int64) error {
			if next == nil || *next != 8 {
				t.Errorf("nextProductID = %v, want 8", next)
			}
			return nil
		})
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.EntityType != model.EntitySlot || e.EntityID != 101 {
				t.Errorf("entry = %+v", e)
			}
			if e.Label != "VM-01 · Slot 1" {
				t.Errorf("label = %q", e.Label)
			}
			// Setting a plan reads as a change from nothing, not from "".
			if e.Before["nextProduct"] != nil || e.After["nextProduct"] != "Gacha B" {
				t.Errorf("plan = %v -> %v", e.Before["nextProduct"], e.After["nextProduct"])
			}
			return nil
		})
	f.slots.EXPECT().Get(gomock.Any(), int64(9), 1).Return(slotAt(5, active(7, "Gacha A"), active(8, "Gacha B")), nil)
	// No BumpCatalogVersion anywhere: that is the point of BR-08.

	got, err := f.uc.UpdateSlot(context.Background(), actor, 9, 1,
		machineuc.SlotInput{Capacity: 8, NextProductID: ptr(int64(8))})
	if err != nil {
		t.Fatal(err)
	}
	if got.NextProduct == nil || got.NextProduct.ID != 8 {
		t.Errorf("slot = %+v, want the plan", got)
	}
	if got.Product == nil || got.Product.ID != 7 {
		t.Errorf("slot = %+v, want the coil unchanged", got)
	}
}

// Capacity may not go below what is in the coil: the count is a physical fact
// and the capacity is a claim about the coil, so the claim has to give.
func TestUpdateSlotRefusesACapacityBelowWhatIsInTheCoil(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 1).Return(slotAt(5, active(7, "Gacha A"), nil), nil)
	f.machines.EXPECT().Get(gomock.Any(), int64(9)).Return(vm(), nil)
	// Nothing is written.

	_, err := f.uc.UpdateSlot(context.Background(), actor, 9, 1, machineuc.SlotInput{Capacity: 4})
	if got := code(t, err); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
	if got := fieldsOf(t, err)["capacity"]; !strings.Contains(got, "5") {
		t.Errorf("fields[capacity] = %q, want the current count named", got)
	}
}

func TestUpdateSlotRefusesAnImpossibleCapacity(t *testing.T) {
	for _, capacity := range []int{0, -1, model.MaxSlotCapacity + 1} {
		t.Run(strings.TrimSpace(itoa(capacity)), func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 1).Return(slotAt(0, nil, nil), nil)
			f.machines.EXPECT().Get(gomock.Any(), int64(9)).Return(vm(), nil)

			_, err := f.uc.UpdateSlot(context.Background(), actor, 9, 1, machineuc.SlotInput{Capacity: capacity})
			if fieldsOf(t, err)["capacity"] == "" {
				t.Error("the refusal must name the capacity")
			}
		})
	}
}

// Only an active product may be planned: planning an archived one would put the
// kiosk back to selling something that is off the list (BR-10).
func TestUpdateSlotRefusesAProductThatCannotBePlanned(t *testing.T) {
	cases := []struct {
		name    string
		product model.Product
		err     error
	}{
		{"no such product", model.Product{}, model.ErrNoRecord},
		{"an archived product", model.Product{ID: 8, Name: "Gacha B", IsActive: false}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 1).Return(slotAt(0, nil, nil), nil)
			f.machines.EXPECT().Get(gomock.Any(), int64(9)).Return(vm(), nil)
			f.products.EXPECT().LockShared(gomock.Any(), int64(8)).Return(tc.product, tc.err)
			// No SetPlan, no log row.

			_, err := f.uc.UpdateSlot(context.Background(), actor, 9, 1,
				machineuc.SlotInput{Capacity: 8, NextProductID: ptr(int64(8))})
			if got := code(t, err); got != model.CodeValidation {
				t.Fatalf("code = %s, want %s", got, model.CodeValidation)
			}
			if fieldsOf(t, err)["nextProductId"] == "" {
				t.Error("the refusal must name the product field")
			}
		})
	}
}

// Planning the product the coil already holds is not a change, so the plan is
// dropped rather than queued as a no-op for the next operator to apply.
func TestUpdateSlotDropsAPlanForWhatIsAlreadyInTheCoil(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 1).Return(slotAt(5, active(7, "Gacha A"), nil), nil)
	f.machines.EXPECT().Get(gomock.Any(), int64(9)).Return(vm(), nil)
	f.products.EXPECT().LockShared(gomock.Any(), int64(7)).Return(*active(7, "Gacha A"), nil)
	f.slots.EXPECT().Get(gomock.Any(), int64(9), 1).Return(slotAt(5, active(7, "Gacha A"), nil), nil)
	// Capacity is unchanged and the plan is dropped, so nothing changed at all:
	// no SetPlan and no log row.

	if _, err := f.uc.UpdateSlot(context.Background(), actor, 9, 1,
		machineuc.SlotInput{Capacity: 8, NextProductID: ptr(int64(7))}); err != nil {
		t.Fatal(err)
	}
}

// Clearing a plan is a change, and the log records it as going back to nothing.
func TestUpdateSlotClearsAPlan(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 1).
		Return(slotAt(5, active(7, "Gacha A"), active(8, "Gacha B")), nil)
	f.machines.EXPECT().Get(gomock.Any(), int64(9)).Return(vm(), nil)
	f.slots.EXPECT().SetPlan(gomock.Any(), int64(101), 8, nil).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Before["nextProduct"] != "Gacha B" || e.After["nextProduct"] != nil {
				t.Errorf("plan = %v -> %v", e.Before["nextProduct"], e.After["nextProduct"])
			}
			return nil
		})
	f.slots.EXPECT().Get(gomock.Any(), int64(9), 1).Return(slotAt(5, active(7, "Gacha A"), nil), nil)

	if _, err := f.uc.UpdateSlot(context.Background(), actor, 9, 1,
		machineuc.SlotInput{Capacity: 8}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateSlotRefusesASlotTheMachineDoesNotHave(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.slots.EXPECT().LockForUpdate(gomock.Any(), int64(9), 61).Return(model.Slot{}, model.ErrNoRecord)

	_, err := f.uc.UpdateSlot(context.Background(), actor, 9, 61, machineuc.SlotInput{Capacity: 8})
	if got := code(t, err); got != model.CodeNotFound {
		t.Errorf("code = %s, want %s", got, model.CodeNotFound)
	}
}

// A storage failure is reported as itself, so an outage does not look to the
// admin like a form filled in wrong.
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	boom := errors.New("the machines table is unreachable")
	f.machines.EXPECT().List(gomock.Any(), int64(0)).Return(nil, boom)

	if _, err := f.uc.List(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}

func ptr(v int64) *int64 { return &v }

func itoa(n int) string {
	if n < 0 {
		return "minus " + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
