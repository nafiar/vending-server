package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"vending/internal/core"
	"vending/internal/security"
)

// TC-13: one visit saves one session with a line per slot, sets the counts and
// appears in the restock history with the operator and the time.
func TestTC13_RestockVisit(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Common", 10000)
	products := []int64{p, p, p, p, p, p}
	mid, key := e.setupMachine("VM-01", 10, products, []int{2, 5, 0, 7, 10, 3})
	opID := e.createOperator("budi", "budi123")

	tok := e.operatorLogin(key, "budi", "budi123")
	var slots core.OperatorSlots
	e.mustOK(e.asOperator(key, tok, "GET", "/api/machine/operator/slots", nil), &slots)
	if len(slots.Slots) != 6 || slots.Slots[3].Qty != 7 || slots.Slots[0].Capacity != 10 {
		t.Fatalf("restock screen: %+v", slots)
	}
	for i := 0; i < 6; i++ { // a six-minute visit: the screen keeps the 2-minute idle timer alive
		e.clock.Advance(time.Minute)
		e.mustOK(e.asOperator(key, tok, "GET", "/api/machine/operator/slots", nil), nil)
	}
	saved := e.restock(key, tok, map[int]int{1: 10, 2: 10, 3: 4, 4: 10, 5: 10, 6: 2})
	if saved.SessionID == 0 || saved.ID == "" {
		t.Fatalf("saved: %+v", saved)
	}
	m := e.machineView(mid)
	for i, want := range []int{10, 10, 4, 10, 10, 2} {
		if m.Slots[i].Qty != want {
			t.Fatalf("slot %d: want %d, got %d", i+1, want, m.Slots[i].Qty)
		}
	}
	var page core.RestockPage
	e.mustOK(e.asAdmin("GET", fmt.Sprintf("/api/admin/restock-sessions?userId=%d", opID), nil), &page)
	if page.Total != 1 {
		t.Fatalf("history: %+v", page)
	}
	v := page.Rows[0]
	if v.Operator.Username != "budi" || len(v.Items) != 6 || v.StartedAt != t0.UnixMilli() || v.FinishedAt != t0.Add(6*time.Minute).UnixMilli() {
		t.Fatalf("visit: %+v", v)
	}
	if v.Items[0].QtyBefore != 2 || v.Items[0].QtyAfter != 10 || v.ItemsAdded != 8+5+4+3+0+0 || v.SlotsChanged != 5 {
		t.Fatalf("lines: %+v", v)
	}
}

// TC-14 to TC-16: counts are whole numbers from 0 to the capacity; anything else
// is refused and nothing is saved. A resend of the same visit is saved once.
func TestTC14_TC16_RestockBoundaries(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Common", 10000)
	mid, key := e.setupMachine("VM-01", 10, []int64{p, p}, []int{5, 5})
	e.createOperator("budi", "budi123")
	tok := e.operatorLogin(key, "budi", "budi123")

	e.restock(key, tok, map[int]int{1: 0, 2: 10})          // TC-14: both limits pass
	for _, bad := range []any{11, -1, 2.5, "", "3", nil} { // TC-15 and TC-16
		body := map[string]any{"id": security.NewUUID(), "items": []map[string]any{{"slotNumber": 1, "qtyAfter": bad}}}
		r := e.expect(e.asOperator(key, tok, "POST", "/api/machine/restock-sessions", body), http.StatusUnprocessableEntity, "INVALID_INPUT")
		if r["fields"] == nil {
			t.Fatalf("%v: want per-slot fields, got %v", bad, r)
		}
	}
	if q := e.slotQty(mid, 1); q != 0 {
		t.Fatalf("a refused visit must save nothing: count %d", q)
	}
	var page core.RestockPage
	e.mustOK(e.asAdmin("GET", "/api/admin/restock-sessions", nil), &page)
	before := page.Total

	same := map[string]any{"id": security.NewUUID(), "items": []map[string]any{{"slotNumber": 1, "qtyAfter": 3}}}
	var a, b core.RestockSaved
	e.mustOK(e.asOperator(key, tok, "POST", "/api/machine/restock-sessions", same), &a)
	e.mustOK(e.asOperator(key, tok, "POST", "/api/machine/restock-sessions", same), &b)
	e.mustOK(e.asAdmin("GET", "/api/admin/restock-sessions", nil), &page)
	if a.SessionID != b.SessionID || page.Total != before+1 {
		t.Fatalf("a resend must be saved once: %+v %+v total %d", a, b, page.Total)
	}
}

// TC-17, TC-18: a planned product change waits for the next visit; until then
// the kiosk keeps showing and selling the current product.
func TestTC17_TC18_PlannedChange(t *testing.T) {
	e := newEnv(t)
	a := e.createProduct("Gacha A", 10000)
	b := e.createProduct("Gacha B", 20000)
	mid, key := e.setupMachine("VM-01", 10, []int64{a}, []int{5})

	e.planSlot(mid, 1, b, 10)
	c := e.catalog(key)
	if c.Slots[0].Product.ID != a {
		t.Fatal("TC-18: the kiosk must keep showing A before the visit")
	}
	e.buy(key, 1)
	if page := e.transactions(""); page.Rows[0].Product.ID != a {
		t.Fatal("TC-18: the kiosk must keep selling A")
	}

	e.createOperator("budi", "budi123")
	tok := e.operatorLogin(key, "budi", "budi123")
	var slots core.OperatorSlots
	e.mustOK(e.asOperator(key, tok, "GET", "/api/machine/operator/slots", nil), &slots)
	if slots.Slots[0].NextProduct == nil || slots.Slots[0].NextProduct.ID != b {
		t.Fatal("the restock screen must show the planned change")
	}
	saved := e.restock(key, tok, map[int]int{1: 8})
	c = e.catalog(key)
	if c.Slots[0].Product.ID != b || c.CatalogVersion != saved.CatalogVersion || !c.Slots[0].Available {
		t.Fatalf("TC-17: after the visit the kiosk shows B: %+v", c)
	}
	var page core.RestockPage
	e.mustOK(e.asAdmin("GET", "/api/admin/restock-sessions", nil), &page)
	if it := page.Rows[0].Items[0]; !it.ProductChanged || *it.ProductID != b || it.QtyAfter != 8 {
		t.Fatalf("the visit line must record the change: %+v", it)
	}
}

// TC-19: five wrong passwords lock the account for five minutes, even against the right password.
func TestTC19_Lockout(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha A", 10000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{1})
	e.createOperator("budi", "budi123")

	login := func(pw string) reply {
		return e.asMachine(key, "POST", "/api/machine/operator/login", map[string]string{"username": "budi", "password": pw})
	}
	for i := 1; i <= 4; i++ {
		r := e.expect(login("salah"), http.StatusUnauthorized, "INVALID_LOGIN")
		if int(r["attemptsLeft"].(float64)) != 5-i {
			t.Fatalf("attempt %d: attemptsLeft %v", i, r["attemptsLeft"])
		}
	}
	e.expect(login("salah"), http.StatusLocked, "LOCKED")
	r := e.expect(login("budi123"), http.StatusLocked, "LOCKED")
	if r["retryInSeconds"].(float64) <= 0 {
		t.Fatalf("retryInSeconds: %v", r)
	}
	e.clock.Advance(5*time.Minute + time.Second)
	e.mustOK(login("budi123"), nil)
	// An unknown username is refused the same way, without attemptsLeft.
	r = e.expect(e.asMachine(key, "POST", "/api/machine/operator/login", map[string]string{"username": "nobody", "password": "x"}), http.StatusUnauthorized, "INVALID_LOGIN")
	if _, ok := r["attemptsLeft"]; ok {
		t.Fatal("an unknown username must not get attemptsLeft")
	}
}

// TC-20, BR-13: operator sessions end after 2 minutes idle and only work on their machine.
func TestTC20_OperatorIdle(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha A", 10000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{1})
	_, key2 := e.setupMachine("VM-02", 10, []int64{p}, []int{1})
	e.createOperator("budi", "budi123")

	tok := e.operatorLogin(key, "budi", "budi123")
	e.clock.Advance(90 * time.Second)
	e.mustOK(e.asOperator(key, tok, "GET", "/api/machine/operator/slots", nil), nil) // still active, and the idle timer restarts
	e.expect(e.asOperator(key2, tok, "GET", "/api/machine/operator/slots", nil), http.StatusUnauthorized, "UNAUTHORIZED")
	tok = e.operatorLogin(key, "budi", "budi123")
	e.clock.Advance(2*time.Minute + time.Second)
	e.expect(e.asOperator(key, tok, "GET", "/api/machine/operator/slots", nil), http.StatusUnauthorized, "UNAUTHORIZED")

	// A deactivated operator can't log in, and an active session ends with the account.
	tok = e.operatorLogin(key, "budi", "budi123")
	var ops []core.OperatorView
	e.mustOK(e.asAdmin("GET", "/api/admin/operators", nil), &ops)
	var budi int64
	for _, o := range ops {
		if o.Username == "budi" {
			budi = o.ID
		}
	}
	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/operators/%d/deactivate", budi), nil), nil)
	e.expect(e.asOperator(key, tok, "GET", "/api/machine/operator/slots", nil), http.StatusUnauthorized, "UNAUTHORIZED")
	e.expect(e.asMachine(key, "POST", "/api/machine/operator/login", map[string]string{"username": "budi", "password": "budi123"}), http.StatusForbidden, "FORBIDDEN")
}

// NFR-05: after sales, failures and visits every slot's count still equals its
// last visit's count minus the DISPENSED sales since then.
func TestReconcileAfterFlows(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha A", 10000)
	_, key := e.setupMachine("VM-01", 10, []int64{p, p, p}, []int{5, 5, 5})

	e.buy(key, 1)
	e.buy(key, 1)
	e.buy(key, 2)
	id := e.startPurchase(key, 3)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "FAILED", "failureReason": "NO_ACK"}), nil)
	e.startPurchase(key, 2) // left PENDING

	var r struct {
		Slots      []core.SlotReconciliation `json:"slots"`
		Mismatches int                       `json:"mismatches"`
	}
	e.mustOK(e.asAdmin("GET", "/api/admin/reconcile", nil), &r)
	if r.Mismatches != 0 || len(r.Slots) != 3 || r.Slots[0].Actual != 3 || r.Slots[0].SoldSince != 2 {
		t.Fatalf("reconcile: %+v", r)
	}
}
