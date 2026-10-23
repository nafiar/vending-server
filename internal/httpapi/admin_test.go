package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"vending/internal/core"
	"vending/internal/seed"
)

// TC-21, BR-13: an operator account can't use the admin web, and an admin
// session ends after 30 minutes idle.
func TestTC21_AdminLogin(t *testing.T) {
	e := newEnv(t)
	e.createOperator("budi", "budi123")
	e.expect(e.do("POST", "/api/admin/login", map[string]string{"username": "budi", "password": "budi123"}, nil), http.StatusForbidden, "FORBIDDEN")
	e.expect(e.do("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "salah"}, nil), http.StatusUnauthorized, "INVALID_LOGIN")
	e.expect(e.do("GET", "/api/admin/me", nil, nil), http.StatusUnauthorized, "UNAUTHORIZED")

	var me core.Me
	e.mustOK(e.asAdmin("GET", "/api/admin/me", nil), &me)
	if me.Username != "admin" || me.Role != "ADMIN" {
		t.Fatalf("me: %+v", me)
	}
	e.clock.Advance(29 * time.Minute)
	e.mustOK(e.asAdmin("GET", "/api/admin/me", nil), nil) // activity restarts the 30 minutes
	e.clock.Advance(30*time.Minute + time.Second)
	e.expect(e.asAdmin("GET", "/api/admin/me", nil), http.StatusUnauthorized, "UNAUTHORIZED")
}

func pngDataURL(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 30, 60, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TC-22, TC-23: valid products are saved (with a stored image), invalid ones are
// refused field by field and nothing is saved.
func TestTC23_ProductValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		body  map[string]any
		field string
	}{
		{map[string]any{"name": "", "price": 10000}, "name"},
		{map[string]any{"name": "X", "price": 0}, "price"},
		{map[string]any{"name": "X", "price": -5000}, "price"},
		{map[string]any{"name": "X", "price": 2.5}, "price"},
		{map[string]any{"name": strings.Repeat("a", 101), "price": 1000}, "name"},
		{map[string]any{"name": "X", "price": 1000, "imageUrl": "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, 3<<20))}, "image"},
		{map[string]any{"name": "X", "price": 1000, "imageUrl": "data:image/gif;base64,R0lGODlhAQABAAAAACw="}, "image"},
		{map[string]any{"name": "X", "price": 1000, "imageUrl": "https://example.com/a.png"}, "image"},
		{map[string]any{"name": "X", "price": 1000, "imageUrl": "data:image/png;base64,bm90IGFuIGltYWdl"}, "image"},
	}
	for _, c := range cases {
		r := e.expect(e.asAdmin("POST", "/api/admin/products", c.body), http.StatusUnprocessableEntity, "VALIDATION")
		if fields := r["fields"].(map[string]any); fields[c.field] == nil {
			t.Errorf("%v: want an error on %s, got %v", c.body["name"], c.field, fields)
		}
	}
	var list []core.ProductView
	e.mustOK(e.asAdmin("GET", "/api/admin/products?includeArchived=1", nil), &list)
	if len(list) != 0 {
		t.Fatalf("nothing may be saved, got %d products", len(list))
	}

	var p core.ProductView
	e.mustOK(e.asAdmin("POST", "/api/admin/products", map[string]any{"name": "  Gacha Rare ", "description": "5 kartu", "price": 25000, "imageUrl": pngDataURL(t, 30, 40)}), &p)
	if p.Name != "Gacha Rare" || p.ImageURL == nil || !strings.HasSuffix(*p.ImageURL, ".png") {
		t.Fatalf("created: %+v", p)
	}
	res, err := http.Get(e.srv.URL + *p.ImageURL)
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("stored image: %v %v", err, res)
	}
	res.Body.Close()
	// Without an image a product gets a placeholder tile.
	var q core.ProductView
	e.mustOK(e.asAdmin("POST", "/api/admin/products", map[string]any{"name": "Gacha Common", "price": 10000}), &q)
	if q.ImageURL == nil || !strings.HasSuffix(*q.ImageURL, ".svg") {
		t.Fatalf("placeholder: %+v", q)
	}
	// Directory listings of the uploads are refused.
	if res, _ := http.Get(e.srv.URL + "/uploads/products/"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("listing: %d", res.StatusCode)
	}
}

// TC-24, TC-25: a product in use can't be archived; an archived product keeps its history.
func TestTC24_TC25_Archive(t *testing.T) {
	e := newEnv(t)
	a := e.createProduct("Gacha A", 10000)
	b := e.createProduct("Gacha B", 20000)
	mid, key := e.setupMachine("VM-01", 10, []int64{a}, []int{5})
	e.buy(key, 1)

	r := e.expect(e.asAdmin("POST", fmt.Sprintf("/api/admin/products/%d/archive", a), nil), http.StatusConflict, "CONFLICT")
	if r["reason"] != "IN_USE" || len(r["slots"].([]any)) != 1 {
		t.Fatalf("in use: %v", r)
	}
	e.planSlot(mid, 1, b, 10)
	e.expect(e.asAdmin("POST", fmt.Sprintf("/api/admin/products/%d/archive", b), nil), http.StatusConflict, "CONFLICT") // planned counts too

	// Swap the coil to B at a visit; A now has history but no slot, so it can be archived.
	e.createOperator("budi", "budi123")
	e.restock(key, e.operatorLogin(key, "budi", "budi123"), map[int]int{1: 5})
	var p core.ProductView
	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/products/%d/archive", a), nil), &p)
	if p.IsActive {
		t.Fatal("archive")
	}
	var active []core.ProductView
	e.mustOK(e.asAdmin("GET", "/api/admin/products", nil), &active)
	if len(active) != 1 || active[0].ID != b {
		t.Fatalf("an archived product is hidden from lists: %+v", active)
	}
	if page := e.transactions(""); page.Rows[0].Product.Name != "Gacha A" {
		t.Fatal("old transactions keep the product's name")
	}
	// An archived product can't be planned, and can be restored.
	e.expect(e.asAdmin("PUT", fmt.Sprintf("/api/admin/machines/%d/slots/1", mid), map[string]any{"capacity": 10, "nextProductId": a}), http.StatusUnprocessableEntity, "VALIDATION")
	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/products/%d/restore", a), nil), &p)
	if !p.IsActive {
		t.Fatal("restore")
	}
}

// TC-26: registering a machine creates its slots and shows the key once.
func TestTC26_RegisterMachine(t *testing.T) {
	e := newEnv(t)
	var r core.MachineWithKey
	e.mustOK(e.asAdmin("POST", "/api/admin/machines", map[string]any{"code": "vm-02", "name": "Kantin", "location": "Lantai 1", "slotCount": 8, "capacity": 10}), &r)
	if r.Machine.Code != "VM-02" || r.Machine.SlotCount != 8 || len(r.Machine.Slots) != 8 || !strings.HasPrefix(r.APIKey, "vmk_") {
		t.Fatalf("created: %+v", r)
	}
	for i, s := range r.Machine.Slots {
		if s.SlotNumber != i+1 || s.Capacity != 10 || s.Qty != 0 || s.Product != nil {
			t.Fatalf("slot %d: %+v", i+1, s)
		}
	}
	again := e.asAdmin("GET", fmt.Sprintf("/api/admin/machines/%d", r.Machine.ID), nil)
	if strings.Contains(string(again.body), r.APIKey) || strings.Contains(string(again.body), "apiKey") {
		t.Fatal("the key must never be shown again")
	}
	if m := e.machineView(r.Machine.ID); m.LastSeenAt != nil || m.EmptySlots != 8 {
		t.Fatalf("a new machine has no contact yet and 8 empty slots: %+v", m)
	}
	e.mustOK(e.asMachine(r.APIKey, "GET", "/api/machine/catalog", nil), nil)
	e.expect(e.asAdmin("POST", "/api/admin/machines", map[string]any{"code": "VM-02", "name": "Lagi", "slotCount": 6, "capacity": 10}), http.StatusUnprocessableEntity, "VALIDATION")
}

// TC-28: slot counts outside 1..60 and capacities outside 1..30, or below the current count, are refused.
func TestTC28_MachineBoundaries(t *testing.T) {
	e := newEnv(t)
	for _, n := range []any{0, 61, 2.5} {
		r := e.expect(e.asAdmin("POST", "/api/admin/machines", map[string]any{"code": "VM-09", "name": "X", "slotCount": n, "capacity": 10}), http.StatusUnprocessableEntity, "VALIDATION")
		if r["fields"].(map[string]any)["slotCount"] == nil {
			t.Fatalf("slotCount %v: %v", n, r)
		}
	}
	e.mustOK(e.asAdmin("POST", "/api/admin/machines", map[string]any{"code": "VM-60", "name": "Besar", "slotCount": 60, "capacity": 30}), nil)
	p := e.createProduct("Gacha A", 10000)
	mid, _ := e.setupMachine("VM-01", 10, []int64{p}, []int{6})
	for _, c := range []int{0, 31, 4} {
		e.expect(e.asAdmin("PUT", fmt.Sprintf("/api/admin/machines/%d/slots/1", mid), map[string]any{"capacity": c, "nextProductId": nil}), http.StatusUnprocessableEntity, "VALIDATION")
	}
	var s core.SlotView
	e.mustOK(e.asAdmin("PUT", fmt.Sprintf("/api/admin/machines/%d/slots/1", mid), map[string]any{"capacity": 6, "nextProductId": nil}), &s)
	if s.Capacity != 6 {
		t.Fatalf("capacity equal to the count is allowed: %+v", s)
	}
}

// TC-29, TC-30: the recap counts only DISPENSED, per machine rows add up to the
// total, and weekly buckets add up to the period.
func TestTC29_TC30_Recap(t *testing.T) {
	e := newEnv(t)
	a := e.createProduct("Gacha A", 10000)
	b := e.createProduct("Gacha B", 25000)
	m1, k1 := e.setupMachine("VM-01", 10, []int64{a, b}, []int{10, 10})
	_, k2 := e.setupMachine("VM-02", 10, []int64{a, b}, []int{10, 10})

	// Day one: 3 sales on VM-01 and 2 on VM-02, plus a cancel, an expiry and a failure.
	e.buy(k1, 1)
	e.buy(k1, 2)
	e.buy(k1, 2)
	e.buy(k2, 1)
	e.buy(k2, 2)
	x := e.startPurchase(k1, 1)
	e.mustOK(e.asMachine(k1, "POST", "/api/machine/transactions/"+x+"/cancel", map[string]string{"reason": "CANCEL"}), nil)
	y := e.startPurchase(k2, 1)
	e.mustOK(e.asMachine(k2, "POST", "/api/machine/transactions/"+y+"/cancel", map[string]string{"reason": "TIMEOUT"}), nil)
	z := e.startPurchase(k2, 2)
	e.mustOK(e.asMachine(k2, "POST", "/api/machine/transactions/"+z+"/pay", nil), nil)
	e.mustOK(e.asMachine(k2, "POST", "/api/machine/transactions/"+z+"/result", map[string]string{"status": "FAILED", "failureReason": "MOTOR_TIMEOUT"}), nil)
	// Eight days later, one more sale on VM-01.
	e.clock.Advance(8 * 24 * time.Hour)
	e.buy(k1, 1)
	e.relogin()

	var r core.Recap
	e.mustOK(e.asAdmin("GET", "/api/admin/recap?from=2026-10-01&to=2026-10-31&groupBy=week", nil), &r)
	want := int64(10000 + 25000 + 25000 + 10000 + 25000 + 10000)
	if r.Totals.Revenue != want || r.Totals.Sales != 6 || r.Totals.Failed != 1 || r.Totals.Cancelled != 2 {
		t.Fatalf("totals: %+v", r.Totals)
	}
	var sumMachines, sumWeeks int64
	for _, m := range r.ByMachine {
		sumMachines += m.Revenue
	}
	for _, p := range r.Series {
		sumWeeks += p.Revenue
	}
	if sumMachines != want || sumWeeks != want {
		t.Fatalf("machines %d and weeks %d must add up to %d", sumMachines, sumWeeks, want)
	}
	if r.Series[0].Period != "2026-09-28" || len(r.Series) != 5 {
		t.Fatalf("weeks start on Monday 28 Sep and cover October: %+v", r.Series)
	}
	if r.TopProducts[0].Label != "Gacha B" || r.TopProducts[0].Revenue != 75000 {
		t.Fatalf("top products: %+v", r.TopProducts)
	}

	// TC-30: only VM-01, monthly, by slot.
	e.mustOK(e.asAdmin("GET", fmt.Sprintf("/api/admin/recap?from=2026-10-01&to=2026-10-31&groupBy=month&breakdown=slot&machineId=%d", m1), nil), &r)
	if r.Totals.Revenue != 70000 || len(r.Series) != 1 || r.Series[0].Revenue != 70000 || len(r.ByMachine) != 1 {
		t.Fatalf("VM-01 only: %+v", r)
	}
	if r.Breakdown[0].Label != "VM-01 · Slot 2 (Gacha B)" || r.Breakdown[0].Sales != 2 {
		t.Fatalf("slot breakdown: %+v", r.Breakdown)
	}

	// The summary tiles: today is day nine, with one sale.
	var s core.Summary
	e.mustOK(e.asAdmin("GET", "/api/admin/recap/summary", nil), &s)
	if s.Today.Sales != 1 || s.Today.Revenue != 10000 || s.Month.Revenue != want || s.Last7.Sales != 1 {
		t.Fatalf("summary: %+v", s)
	}
	// Bad filters are validation errors, not server errors.
	e.expect(e.asAdmin("GET", "/api/admin/recap?groupBy=year", nil), http.StatusUnprocessableEntity, "VALIDATION")
	e.expect(e.asAdmin("GET", "/api/admin/recap?from=2026-10-31&to=2026-10-01", nil), http.StatusUnprocessableEntity, "VALIDATION")
	e.expect(e.asAdmin("GET", "/api/admin/transactions?machineId=abc", nil), http.StatusUnprocessableEntity, "VALIDATION")
}

// TC-31: a price change writes one change-log row with who, when, before and after.
func TestTC31_ChangeLog(t *testing.T) {
	e := newEnv(t)
	p := e.createProduct("Gacha Rare", 25000)
	e.clock.Advance(time.Hour)
	e.relogin()
	e.mustOK(e.asAdmin("PUT", fmt.Sprintf("/api/admin/products/%d", p), map[string]any{"name": "Gacha Rare", "price": 30000}), nil)
	e.mustOK(e.asAdmin("PUT", fmt.Sprintf("/api/admin/products/%d", p), map[string]any{"name": "Gacha Rare", "price": 30000}), nil) // no change: no row

	var logs []core.AuditView
	e.mustOK(e.asAdmin("GET", "/api/admin/audit-logs?entityType=PRODUCT", nil), &logs)
	if len(logs) != 2 {
		t.Fatalf("want CREATE and one UPDATE, got %d", len(logs))
	}
	u := logs[0]
	var before, after map[string]any
	_ = json.Unmarshal(u.Before, &before)
	_ = json.Unmarshal(u.After, &after)
	if u.Action != "UPDATE" || u.User.Name != "Admin Test" || u.CreatedAt != t0.Add(time.Hour).UnixMilli() ||
		len(before) != 1 || before["price"] != 25000.0 || len(after) != 1 || after["price"] != 30000.0 {
		t.Fatalf("update row: %+v before=%s after=%s", u, u.Before, u.After)
	}

	// Operators, machines and slots are logged too.
	id := e.createOperator("budi", "budi123")
	var pw map[string]string
	e.mustOK(e.asAdmin("POST", fmt.Sprintf("/api/admin/operators/%d/reset-password", id), nil), &pw)
	if len(pw["temporaryPassword"]) != 10 {
		t.Fatalf("temporary password: %v", pw)
	}
	mid, key := e.createMachine("VM-01", 2, 10)
	e.planSlot(mid, 1, p, 8)
	e.mustOK(e.asAdmin("GET", "/api/admin/audit-logs", nil), &logs)
	seen := map[string]bool{}
	for _, l := range logs {
		seen[l.Action+" "+l.EntityType] = true
	}
	for _, want := range []string{"CREATE USER", "UPDATE USER", "CREATE MACHINE", "UPDATE SLOT", "CREATE PRODUCT", "UPDATE PRODUCT"} {
		if !seen[want] {
			t.Errorf("missing %s in %v", want, seen)
		}
	}
	// The temporary password works on the kiosk.
	e.mustOK(e.asMachine(key, "POST", "/api/machine/operator/login", map[string]string{"username": "budi", "password": pw["temporaryPassword"]}), nil)
}

// The demo seed builds a consistent history: every slot reconciles.
func TestSeedDemo(t *testing.T) {
	e := newEnv(t)
	if _, err := seed.Demo(t.Context(), e.svc, false); err == nil {
		t.Fatal("seeding a database that has an admin must be refused without -reset")
	}
	res, err := seed.Demo(t.Context(), e.svc, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transactions < 1000 || res.Visits < 100 || len(res.MachineKeys) != 3 {
		t.Fatalf("seed: %+v", res)
	}
	rows, err := e.svc.Reconcile(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if !r.Matches {
			t.Errorf("slot %s/%d: expected %d, actual %d", r.MachineCode, r.SlotNumber, r.Expected, r.Actual)
		}
	}
	e.mustOK(e.asMachine(res.MachineKeys["VM-01"], "GET", "/api/machine/catalog", nil), nil)
}

// A demo reset from the admin web keeps the machine keys, so a configured kiosk keeps working.
func TestDemoReset(t *testing.T) {
	e := newEnv(t)
	res, err := seed.Demo(t.Context(), e.svc, true)
	if err != nil {
		t.Fatal(err)
	}
	key := res.MachineKeys["VM-01"]
	var login core.LoginResult
	e.mustOK(e.do("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "admin123"}, nil), &login)
	e.admin = login.Token

	r := e.asAdmin("GET", "/api/admin/me", nil)
	if r.json()["demoMode"] != true {
		t.Fatalf("me must report demo mode: %s", r.body)
	}
	e.mustOK(e.asAdmin("POST", "/api/admin/products", map[string]any{"name": "Barang Uji", "price": 1000}), nil)
	e.mustOK(e.asAdmin("POST", "/api/admin/demo/reset", nil), nil)
	e.expect(e.asAdmin("GET", "/api/admin/me", nil), http.StatusUnauthorized, "UNAUTHORIZED") // every session ended
	e.mustOK(e.asMachine(key, "GET", "/api/machine/catalog", nil), nil)                       // the key survived
	e.mustOK(e.do("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "admin123"}, nil), &login)
	e.admin = login.Token
	var list []core.ProductView
	e.mustOK(e.asAdmin("GET", "/api/admin/products?includeArchived=1", nil), &list)
	for _, p := range list {
		if p.Name == "Barang Uji" {
			t.Fatal("the reset must remove what was added")
		}
	}
}

// Unknown API paths answer JSON, the health check reports the database, and the admin web is served.
func TestRoutingBasics(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do("GET", "/api/nope", nil, nil), http.StatusNotFound, "NOT_FOUND")
	e.expect(e.do("DELETE", "/api/admin/products", nil, nil), http.StatusNotFound, "NOT_FOUND")
	r := e.do("GET", "/api/health", nil, nil)
	if r.status != 200 || r.json()["ok"] != true {
		t.Fatalf("health: %d %s", r.status, r.body)
	}
	res, err := http.Get(e.srv.URL + "/")
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("admin web: %v %v", err, res)
	}
	res.Body.Close()
	e.expect(e.do("POST", "/api/admin/products", "not json", map[string]string{"Authorization": "Bearer " + e.admin}), http.StatusBadRequest, "BAD_REQUEST")
}
