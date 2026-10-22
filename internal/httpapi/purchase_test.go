package httpapi_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"vending/internal/core"
	"vending/internal/security"
)

// TC-01: a whole purchase turns one coil, ends DISPENSED, lowers the count and
// shows up in the history and the recap at its price.
func TestTC01_Purchase(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	mid, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})

	c := e.catalog(key)
	if len(c.Slots) != 1 || !c.Slots[0].Available || c.Slots[0].Product.Price != 25000 {
		t.Fatalf("catalog: %+v", c)
	}
	id := security.NewUUID()
	var created core.CreatedTransaction
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions", map[string]any{"id": id, "slotNumber": 1, "catalogVersion": c.CatalogVersion}), &created)
	if created.Status != "PENDING" || created.Amount != 25000 || created.ExpiresInSeconds != 60 {
		t.Fatalf("created: %+v", created)
	}
	var st core.TransactionState
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), &st)
	if st.Status != "PAID" || st.PaidAt == nil {
		t.Fatalf("pay: %+v", st)
	}
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "DISPENSED"}), &st)
	if st.Status != "DISPENSED" {
		t.Fatalf("result: %+v", st)
	}
	if q := e.slotQty(mid, 1); q != 4 {
		t.Fatalf("count after a sale: want 4, got %d", q)
	}
	page := e.transactions("status=DISPENSED")
	if page.Total != 1 || page.DispensedRevenue != 25000 || page.Rows[0].ID != id {
		t.Fatalf("history: %+v", page)
	}
	var recap core.Recap
	e.mustOK(e.asAdmin("GET", "/api/admin/recap", nil), &recap)
	if recap.Totals.Revenue != 25000 || recap.Totals.Sales != 1 {
		t.Fatalf("recap totals: %+v", recap.Totals)
	}
}

// TC-03, TC-04: cancel and the 60 s timeout leave the count alone and never count as revenue.
func TestTC03_TC04_CancelAndTimeout(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Common", 10000)
	mid, key := e.setupMachine("VM-01", 10, []int64{p}, []int{3})

	a := e.startPurchase(key, 1)
	var st core.TransactionState
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+a+"/cancel", map[string]string{"reason": "CANCEL"}), &st)
	if st.Status != "CANCELLED" {
		t.Fatalf("cancel: %+v", st)
	}
	b := e.startPurchase(key, 1)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+b+"/cancel", map[string]string{"reason": "TIMEOUT"}), &st)
	if st.Status != "EXPIRED" {
		t.Fatalf("timeout: %+v", st)
	}
	// An abandoned screen that never calls cancel is expired by the job (BR-06).
	c := e.startPurchase(key, 1)
	e.clock.Advance(61 * time.Second)
	if n, err := e.svc.ExpirePending(t.Context()); err != nil || n != 1 {
		t.Fatalf("expire job: n=%d err=%v", n, err)
	}
	e.mustOK(e.asMachine(key, "GET", "/api/machine/transactions/"+c, nil), &st)
	if st.Status != "EXPIRED" {
		t.Fatalf("job: %+v", st)
	}
	if q := e.slotQty(mid, 1); q != 3 {
		t.Fatalf("count must not change: got %d", q)
	}
	if page := e.transactions(""); page.DispensedRevenue != 0 || page.Total != 3 {
		t.Fatalf("history: %+v", page)
	}
}

// TC-05: paying 61 s after creation is refused and the coil never turns.
func TestTC05_PayAfterWindow(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Common", 10000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{3})

	id := e.startPurchase(key, 1)
	e.clock.Advance(61 * time.Second)
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), http.StatusGone, "EXPIRED")
	var st core.TransactionState
	e.mustOK(e.asMachine(key, "GET", "/api/machine/transactions/"+id, nil), &st)
	if st.Status != "EXPIRED" {
		t.Fatalf("want EXPIRED, got %+v", st)
	}
	// Exactly 60 s is still inside the window.
	id2 := e.startPurchase(key, 1)
	e.clock.Advance(60 * time.Second)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id2+"/pay", nil), nil)
}

// TC-06: a failed dispense (no END from the board) is FAILED with the reason,
// keeps the count, blocks the slot until the next visit and is listed as a problem.
func TestTC06_FailedDispense(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	mid, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})
	before := e.catalog(key).CatalogVersion

	id := e.startPurchase(key, 1)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil)
	var st core.TransactionState
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "FAILED", "failureReason": "MOTOR_TIMEOUT"}), &st)
	if st.Status != "FAILED" || st.FailureReason == nil || *st.FailureReason != "MOTOR_TIMEOUT" {
		t.Fatalf("result: %+v", st)
	}
	c := e.catalog(key)
	if c.Slots[0].Available || c.CatalogVersion != before+1 {
		t.Fatalf("slot must be blocked and the version raised: %+v", c)
	}
	m := e.machineView(mid)
	if m.Slots[0].Qty != 5 || !m.Slots[0].NeedsCheck || m.BlockedSlots != 1 {
		t.Fatalf("slot: %+v", m.Slots[0])
	}
	// Buying from the blocked slot is refused.
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions", map[string]any{"id": security.NewUUID(), "slotNumber": 1, "catalogVersion": c.CatalogVersion}),
		http.StatusConflict, "SOLD_OUT")
	if page := e.transactions("flag=problem"); page.Total != 1 || page.DispensedRevenue != 0 {
		t.Fatalf("problem filter: %+v", page)
	}
	// The next visit clears the block.
	e.createOperator("budi", "budi123")
	tok := e.operatorLogin(key, "budi", "budi123")
	e.restock(key, tok, map[int]int{1: 5})
	if !e.catalog(key).Slots[0].Available {
		t.Fatal("a visit must clear the block")
	}
}

// TC-08 and NFR-03: repeating a request never creates a second transaction or decrement.
func TestTC08_RepeatsChangeNothing(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	mid, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})
	v := e.catalog(key).CatalogVersion

	id := security.NewUUID()
	body := map[string]any{"id": id, "slotNumber": 1, "catalogVersion": v}
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions", body), nil)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions", body), nil) // a retried create
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil) // a retried pay
	for i := 0; i < 3; i++ {
		e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "DISPENSED"}), nil)
	}
	if q := e.slotQty(mid, 1); q != 4 {
		t.Fatalf("one sale must lower the count once: got %d", q)
	}
	if page := e.transactions(""); page.Total != 1 {
		t.Fatalf("want one transaction, got %d", page.Total)
	}
	// A different result for a finished transaction is refused.
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "FAILED", "failureReason": "NO_ACK"}),
		http.StatusConflict, "CONFLICT")
}

// TC-09: the price is copied when the transaction is created; a later change doesn't touch it.
func TestTC09_PriceSnapshot(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})

	id := e.startPurchase(key, 1)
	e.mustOK(e.asAdmin("PUT", fmt.Sprintf("/api/admin/products/%d", p), map[string]any{"name": "Gacha Rare", "price": 30000}), nil)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "DISPENSED"}), nil)
	if page := e.transactions(""); page.Rows[0].Price != 25000 || page.DispensedRevenue != 25000 {
		t.Fatalf("want the old price 25000: %+v", page.Rows[0])
	}
}

// TC-10: a purchase against an old catalog version is refused with the fresh catalog.
func TestTC10_StaleCatalog(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})
	old := e.catalog(key).CatalogVersion

	e.mustOK(e.asAdmin("PUT", fmt.Sprintf("/api/admin/products/%d", p), map[string]any{"name": "Gacha Rare", "price": 30000}), nil)
	body := e.expect(e.asMachine(key, "POST", "/api/machine/transactions", map[string]any{"id": security.NewUUID(), "slotNumber": 1, "catalogVersion": old}),
		http.StatusConflict, "CATALOG_STALE")
	fresh := body["catalog"].(map[string]any)
	if int64(fresh["catalogVersion"].(float64)) != old+1 {
		t.Fatalf("fresh catalog version: %v", fresh["catalogVersion"])
	}
	price := fresh["slots"].([]any)[0].(map[string]any)["product"].(map[string]any)["price"].(float64)
	if price != 30000 {
		t.Fatalf("fresh catalog must show the new price, got %v", price)
	}
	if page := e.transactions(""); page.Total != 0 {
		t.Fatal("nothing may be created on a stale catalog")
	}
}

// TC-11: with a count of 1, a second transaction for the slot is sold out while the first is open.
func TestTC11_LastItem(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Legendary", 50000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{1})

	e.startPurchase(key, 1)
	if e.catalog(key).Slots[0].Available {
		t.Fatal("the open transaction holds the last item")
	}
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions", map[string]any{"id": security.NewUUID(), "slotNumber": 1, "catalogVersion": e.catalog(key).CatalogVersion}),
		http.StatusConflict, "SOLD_OUT")
	// Once the first expires, the item is free again.
	e.clock.Advance(61 * time.Second)
	e.startPurchase(key, 1)
}

// BR-04 under load: concurrent purchases of one slot never sell more than its count.
func TestConcurrentPurchasesNeverOversell(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{3})
	v := e.catalog(key).CatalogVersion

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, soldOut := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := e.do("POST", "/api/machine/transactions", map[string]any{"id": security.NewUUID(), "slotNumber": 1, "catalogVersion": v},
				map[string]string{"X-Machine-Key": key})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.status == http.StatusOK:
				ok++
			case r.code() == "SOLD_OUT":
				soldOut++
			default:
				t.Errorf("unexpected reply %d: %s", r.status, r.body)
			}
		}()
	}
	wg.Wait()
	if ok != 3 || soldOut != 9 {
		t.Fatalf("want 3 created and 9 sold out, got %d and %d", ok, soldOut)
	}
}

// TC-12: "Produk tidak keluar?" keeps the sale DISPENSED, adds the report time,
// and the sale appears under the customer-reported filter while still counting as revenue.
func TestTC12_CustomerReport(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})

	id := e.buy(key, 1)
	e.clock.Advance(20 * time.Second)
	var rep core.ReportResult
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/report", nil), &rep)
	if !rep.Reported {
		t.Fatal("not reported")
	}
	page := e.transactions("flag=reported")
	if page.Total != 1 || page.Rows[0].Status != "DISPENSED" || page.Rows[0].CustomerReportedAt == nil || page.DispensedRevenue != 25000 {
		t.Fatalf("reported filter: %+v", page)
	}
	// A failed or unpaid transaction can't be reported; neither can an old sale.
	pending := e.startPurchase(key, 1)
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions/"+pending+"/report", nil), http.StatusConflict, "NOT_REPORTABLE")
	old := e.buy(key, 1)
	e.clock.Advance(6 * time.Minute)
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions/"+old+"/report", nil), http.StatusConflict, "REPORT_WINDOW_CLOSED")
}

// TC-37 / BR-14: an agent that restarted mid-vend reports AGENT_RESTART; the
// sale is FAILED, flagged for review, and the slot is blocked.
func TestTC37_AgentRestart(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	_, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})

	id := e.startPurchase(key, 1)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil)
	// After the restart the agent asks where the transaction stopped: PAID, no result yet.
	var st core.TransactionState
	e.mustOK(e.asMachine(key, "GET", "/api/machine/transactions/"+id, nil), &st)
	if st.Status != "PAID" {
		t.Fatalf("status: %+v", st)
	}
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "FAILED", "failureReason": "AGENT_RESTART"}), &st)
	if st.Status != "FAILED" || *st.FailureReason != "AGENT_RESTART" {
		t.Fatalf("result: %+v", st)
	}
	if e.catalog(key).Slots[0].Available {
		t.Fatal("slot must be blocked for review")
	}
	// A result without a reason, or with an unknown status, is refused.
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "FAILED"}), http.StatusUnprocessableEntity, "INVALID_INPUT")
	e.expect(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "DONE"}), http.StatusUnprocessableEntity, "INVALID_INPUT")
}

// TC-27, BR-12: a machine key works only for its own machine; a deactivated machine's key is refused.
func TestTC27_MachineKeys(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	_, key1 := e.setupMachine("VM-01", 10, []int64{p}, []int{5})
	id2, key2 := e.setupMachine("VM-02", 10, []int64{p}, []int{5})

	tx := e.startPurchase(key1, 1)
	e.expect(e.asMachine(key2, "POST", "/api/machine/transactions/"+tx+"/pay", nil), http.StatusNotFound, "NOT_FOUND")
	e.expect(e.asMachine(key2, "GET", "/api/machine/transactions/"+tx, nil), http.StatusNotFound, "NOT_FOUND")
	e.expect(e.asMachine("vmk_wrong", "GET", "/api/machine/catalog", nil), http.StatusUnauthorized, "INVALID_MACHINE_KEY")
	e.expect(e.do("GET", "/api/machine/catalog", nil, nil), http.StatusUnauthorized, "INVALID_MACHINE_KEY")

	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/machines/%d/deactivate", id2), nil), nil)
	e.expect(e.asMachine(key2, "GET", "/api/machine/catalog", nil), http.StatusForbidden, "MACHINE_INACTIVE")
	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/machines/%d/activate", id2), nil), nil)
	e.mustOK(e.asMachine(key2, "GET", "/api/machine/catalog", nil), nil)

	// A rotated key replaces the old one at once.
	var r core.MachineWithKey
	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/machines/%d/rotate-key", id2), nil), &r)
	e.expect(e.asMachine(key2, "GET", "/api/machine/catalog", nil), http.StatusUnauthorized, "INVALID_MACHINE_KEY")
	e.mustOK(e.asMachine(r.APIKey, "GET", "/api/machine/catalog", nil), nil)
}

// TC-33, FR-28: every agent call and the heartbeat update the machine's last contact.
func TestTC33_LastContact(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	mid, key := e.setupMachine("VM-01", 10, []int64{p}, []int{5})

	e.clock.Advance(10 * time.Minute)
	if m := e.machineView(mid); *m.LastSeenAt != t0.UnixMilli() {
		t.Fatalf("last contact should still be the setup time: %d", *m.LastSeenAt)
	}
	var hb core.HeartbeatResult
	e.mustOK(e.asMachine(key, "POST", "/api/machine/heartbeat", nil), &hb)
	if !hb.OK || hb.CatalogVersion == 0 {
		t.Fatalf("heartbeat: %+v", hb)
	}
	if m := e.machineView(mid); *m.LastSeenAt != t0.Add(10*time.Minute).UnixMilli() {
		t.Fatalf("heartbeat must update the last contact: %d", *m.LastSeenAt)
	}
}
