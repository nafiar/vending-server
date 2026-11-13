package demo

// An internal test, so it can call build directly. What is worth checking here
// is not the writing -- the end-to-end suite loads the real thing into real
// MySQL -- but that the data it builds is consistent with itself. That is the
// only reason this package simulates events instead of inventing rows: a
// dataset made up per table looks right on every screen and contradicts itself
// across two, and the bookkeeping check (NFR-05) is where that shows.

import (
	"context"
	"errors"
	"testing"
	"time"

	"vending/internal/common/clock"
	"vending/internal/model"
)

var jakarta = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		panic(err)
	}
	return loc
}()

// 22 Oct 2026, 10:00 WIB.
var t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)

// fakeStore records what it was asked to do and writes nothing.
type fakeStore struct {
	keys     map[string]string
	occupied int
	err      error

	readKeys bool
	wiped    bool
	written  *model.DemoData
	// order records the calls, because a wipe after a write would destroy the
	// data it had just loaded.
	order []string
}

func (f *fakeStore) MachineKeyHashes(context.Context) (map[string]string, error) {
	f.readKeys = true
	f.order = append(f.order, "keys")
	return f.keys, f.err
}

func (f *fakeStore) CountOccupied(context.Context) (int, error) {
	f.order = append(f.order, "count")
	return f.occupied, f.err
}

func (f *fakeStore) Wipe(context.Context) error {
	f.wiped = true
	f.order = append(f.order, "wipe")
	return f.err
}

func (f *fakeStore) Write(_ context.Context, d model.DemoData) error {
	f.written = &d
	f.order = append(f.order, "write")
	return f.err
}

// fakeImages hands back a URL per saved image without touching a disk.
type fakeImages struct{ saved int }

func (f *fakeImages) Save(_ []byte, ext string) (string, error) {
	f.saved++
	return "/uploads/demo" + ext, nil
}

func (f *fakeImages) Delete(string) error { return nil }

func newService(store *fakeStore) *Service {
	return New(store, &fakeImages{}, clock.New(func() time.Time { return t0 }), jakarta)
}

// Seeding over data somebody might care about is refused, and nothing is
// touched: the refusal has to come before the first write, not after it.
func TestLoadRefusesANonEmptyDatabase(t *testing.T) {
	store := &fakeStore{occupied: 3}
	_, err := newService(store).Load(context.Background(), false)
	if !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("err = %v, want ErrNotEmpty", err)
	}
	if store.written != nil || store.wiped {
		t.Fatalf("nothing may be written or wiped: %v", store.order)
	}
}

// A reset reads the machine keys first and only then empties the tables,
// because afterwards there is nothing left to read them from. A machine coming
// back under the same code keeps its key, so a kiosk already configured with it
// keeps working -- and no new key is reported for it, since the old one is
// still the one that works (FR-23).
func TestResetKeepsKnownMachineKeysInOrder(t *testing.T) {
	store := &fakeStore{keys: map[string]string{"VM-01": "hash-of-the-old-key"}}
	res, err := newService(store).Load(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"keys", "wipe", "write"}; len(store.order) != 3 ||
		store.order[0] != want[0] || store.order[1] != want[1] || store.order[2] != want[2] {
		t.Fatalf("order = %v, want %v", store.order, want)
	}
	if res.MachineKeys["VM-01"] != "" {
		t.Errorf("VM-01 kept its key, so no new key may be reported: %q", res.MachineKeys["VM-01"])
	}
	for _, code := range []string{"VM-02", "VM-03"} {
		if res.MachineKeys[code] == "" {
			t.Errorf("%s is new, so its key must be shown once", code)
		}
	}
	// The kept hash is the one written back, not a new one.
	for _, m := range store.written.Machines {
		if m.Code == "VM-01" && m.KeyHash != "hash-of-the-old-key" {
			t.Errorf("VM-01 key hash = %q, want the kept one", m.KeyHash)
		}
	}
}

// The dataset has to hold together: every row that names another row must name
// one that is in the same dataset. A dangling slot id is a foreign-key error
// thousands of inserts into a load, which is a miserable thing to debug.
func TestBuiltDataIsReferentiallyWhole(t *testing.T) {
	data, res, err := newService(&fakeStore{}).build(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}

	products := map[int64]bool{}
	for _, p := range data.Products {
		products[p.ID] = true
	}
	users := map[int64]bool{}
	for _, u := range data.Users {
		users[u.ID] = true
	}
	machines := map[int64]bool{}
	for _, m := range data.Machines {
		machines[m.ID] = true
	}
	slots := map[int64]int64{} // slot id -> machine id
	for _, s := range data.Slots {
		if !machines[s.MachineID] {
			t.Fatalf("slot %d belongs to machine %d, which is not in the data", s.ID, s.MachineID)
		}
		if !products[s.ProductID] {
			t.Fatalf("slot %d holds product %d, which is not in the data", s.ID, s.ProductID)
		}
		if s.NextProductID != nil && !products[*s.NextProductID] {
			t.Fatalf("slot %d plans product %d, which is not in the data", s.ID, *s.NextProductID)
		}
		if s.Qty < 0 || s.Qty > s.Capacity {
			t.Fatalf("slot %d holds %d of a capacity of %d", s.ID, s.Qty, s.Capacity)
		}
		slots[s.ID] = s.MachineID
	}

	if len(data.Transactions) < 1000 {
		t.Fatalf("got %d transactions, want a demo worth looking at", len(data.Transactions))
	}
	for _, tx := range data.Transactions {
		if slots[tx.SlotID] != tx.MachineID {
			t.Fatalf("transaction %s came out of slot %d, which is not on machine %d", tx.ID, tx.SlotID, tx.MachineID)
		}
		if !products[tx.ProductID] {
			t.Fatalf("transaction %s sold product %d, which is not in the data", tx.ID, tx.ProductID)
		}
		if !model.TransactionStatuses[tx.Status] {
			t.Fatalf("transaction %s is in state %q", tx.ID, tx.Status)
		}
		// A final state has an ending, and only a FAILED one has a reason (BR-07).
		if model.IsFinal(tx.Status) && tx.FinishedAt == nil {
			t.Fatalf("transaction %s is %s but never finished", tx.ID, tx.Status)
		}
		if (tx.Status == model.StatusFailed) != (tx.FailureReason != nil) {
			t.Fatalf("transaction %s is %s with reason %v", tx.ID, tx.Status, tx.FailureReason)
		}
		// A sale a customer complained about is still a sale (BR-03, FR-09).
		if tx.CustomerReportedAt != nil && tx.Status != model.StatusDispensed {
			t.Fatalf("transaction %s was reported but is %s", tx.ID, tx.Status)
		}
	}

	if len(data.Visits) < 100 {
		t.Fatalf("got %d visits, want a demo worth looking at", len(data.Visits))
	}
	for _, v := range data.Visits {
		if !machines[v.MachineID] || !users[v.UserID] {
			t.Fatalf("visit %d is to machine %d by user %d", v.ID, v.MachineID, v.UserID)
		}
		if !v.FinishedAt.After(v.StartedAt) {
			t.Fatalf("visit %d finished at %s, started at %s", v.ID, v.FinishedAt, v.StartedAt)
		}
		if len(v.Items) == 0 {
			t.Fatalf("visit %d has no lines, so it is not a visit", v.ID)
		}
		for _, it := range v.Items {
			if _, ok := slots[it.SlotID]; !ok {
				t.Fatalf("visit %d has a line for slot %d, which is not in the data", v.ID, it.SlotID)
			}
		}
	}

	for _, a := range data.Audits {
		if !users[a.ActorID] {
			t.Fatalf("change-log row by user %d, who is not in the data", a.ActorID)
		}
		if a.Action == model.ActionCreate && a.Before != nil {
			t.Fatalf("a CREATE has nothing before it: %+v", a)
		}
		if a.After == nil {
			t.Fatalf("change-log row with no after: %+v", a)
		}
	}

	if res.Transactions != len(data.Transactions) || res.Visits != len(data.Visits) {
		t.Fatalf("the report says %d/%d, the data has %d/%d",
			res.Transactions, res.Visits, len(data.Transactions), len(data.Visits))
	}
}

// Nothing in the data may be in the future, and the history has to reach back
// far enough to fill the recap's default span.
func TestBuiltDataSitsInThePast(t *testing.T) {
	data, _, err := newService(&fakeStore{}).build(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	oldest := t0
	for _, tx := range data.Transactions {
		if tx.CreatedAt.After(t0) {
			t.Fatalf("transaction %s was created in the future, at %s", tx.ID, tx.CreatedAt)
		}
		if tx.FinishedAt != nil && tx.FinishedAt.Before(tx.CreatedAt) {
			t.Fatalf("transaction %s finished before it started", tx.ID)
		}
		if tx.CreatedAt.Before(oldest) {
			oldest = tx.CreatedAt
		}
	}
	if days := t0.Sub(oldest).Hours() / 24; days < 60 {
		t.Fatalf("the history only reaches back %.0f days; the recap defaults to 30 and compares months", days)
	}
}

// The generator is seeded with a constant, so two demo servers built from this
// code tell the same story. A demo that looks different every time is one
// nobody can be walked through.
func TestBuildIsDeterministic(t *testing.T) {
	a, _, err := newService(&fakeStore{}).build(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := newService(&fakeStore{}).build(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Transactions) != len(b.Transactions) || len(a.Visits) != len(b.Visits) {
		t.Fatalf("two builds differ: %d/%d then %d/%d",
			len(a.Transactions), len(a.Visits), len(b.Transactions), len(b.Visits))
	}
	for i := range a.Slots {
		if a.Slots[i].Qty != b.Slots[i].Qty || a.Slots[i].NeedsCheck != b.Slots[i].NeedsCheck {
			t.Fatalf("slot %d ended differently between builds", a.Slots[i].ID)
		}
	}
}
