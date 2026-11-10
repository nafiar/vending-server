package httpapi_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"vending/internal/core"
	"vending/internal/security"
	"vending/internal/seed"
)

// The API contract, captured as a shape: for every route, the status, the error
// code when there is one, and every JSON field path with the type(s) seen there.
//
// The admin web (web/admin/js/api.js) and the Kiosk Agent depend on these field
// names and on the status each error code maps to. Values (ids, tokens, times,
// random image names) differ between runs, so the golden file records the shape
// rather than the bytes — that is what a layer move must not change.
//
// Regenerate after an intended contract change, and read the diff:
//
//	go test ./internal/httpapi -run TestAPIContract -update
var update = flag.Bool("update", false, "rewrite testdata/contract.golden")

// pruned paths are recorded as their JSON kind without recursing, because their
// contents are free-form by design (an audit row's before/after payload).
var pruned = map[string]bool{"[].before": true, "[].after": true}

// shape walks a decoded JSON value and records "path -> type" for every leaf.
func shape(v any, prefix string, out map[string]map[string]bool) {
	add := func(t string) {
		if out[prefix] == nil {
			out[prefix] = map[string]bool{}
		}
		out[prefix][t] = true
	}
	if pruned[prefix] {
		if v == nil {
			add("null")
		} else {
			add("opaque")
		}
		return
	}
	switch x := v.(type) {
	case nil:
		add("null")
	case bool:
		add("bool")
	case float64:
		add("number")
	case string:
		add("string")
	case []any:
		if len(x) == 0 {
			add("empty-array")
			return
		}
		for _, el := range x {
			shape(el, prefix+"[]", out)
		}
	case map[string]any:
		if len(x) == 0 {
			add("empty-object")
			return
		}
		for k, el := range x {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			shape(el, p, out)
		}
	default:
		add(fmt.Sprintf("unexpected-%T", x))
	}
}

// record renders one reply as the golden file's block for it.
func record(label string, r reply) string {
	var b strings.Builder
	head := fmt.Sprintf("%s -> %d", label, r.status)
	if r.status >= 400 {
		head += " " + r.code()
	}
	b.WriteString(head + "\n")

	var v any
	if err := json.Unmarshal(r.body, &v); err != nil {
		b.WriteString("  <not json: " + strings.TrimSpace(string(r.body)) + ">\n")
		return b.String()
	}
	out := map[string]map[string]bool{}
	shape(v, "", out)
	paths := make([]string, 0, len(out))
	for p := range out {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		types := make([]string, 0, len(out[p]))
		for t := range out[p] {
			types = append(types, t)
		}
		sort.Strings(types)
		name := p
		if name == "" {
			name = "<root>"
		}
		b.WriteString("  " + name + ": " + strings.Join(types, "|") + "\n")
	}
	return b.String()
}

// TestAPIContract drives every route and compares the whole collected contract
// against testdata/contract.golden.
func TestAPIContract(t *testing.T) {
	e := newEnv(t)
	res, err := seed.Demo(t.Context(), e.svc, true)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	capture := func(label string, r reply) reply {
		blocks = append(blocks, record(label, r))
		return r
	}

	// ---------------------------------------------------------------- open

	capture("GET /api/health", e.do("GET", "/api/health", nil, nil))

	var login core.LoginResult
	r := capture("POST /api/admin/login", e.do("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "admin123"}, nil))
	e.mustOK(r, &login)
	e.admin = login.Token

	// ---------------------------------------------------------------- admin reads (seeded, non-empty)

	capture("GET /api/admin/me", e.asAdmin("GET", "/api/admin/me", nil))
	capture("GET /api/admin/recap/summary", e.asAdmin("GET", "/api/admin/recap/summary", nil))
	capture("GET /api/admin/recap", e.asAdmin("GET", "/api/admin/recap?groupBy=day&breakdown=product", nil))
	capture("GET /api/admin/transactions", e.asAdmin("GET", "/api/admin/transactions?page=1&pageSize=5", nil))
	capture("GET /api/admin/restock-sessions", e.asAdmin("GET", "/api/admin/restock-sessions?page=1&pageSize=5", nil))
	capture("GET /api/admin/products", e.asAdmin("GET", "/api/admin/products?includeArchived=1", nil))
	capture("GET /api/admin/machines", e.asAdmin("GET", "/api/admin/machines", nil))
	capture("GET /api/admin/operators", e.asAdmin("GET", "/api/admin/operators", nil))
	capture("GET /api/admin/audit-logs", e.asAdmin("GET", "/api/admin/audit-logs", nil))
	capture("GET /api/admin/reconcile", e.asAdmin("GET", "/api/admin/reconcile", nil))

	var machines []core.MachineView
	e.mustOK(e.asAdmin("GET", "/api/admin/machines", nil), &machines)
	if len(machines) == 0 {
		t.Fatal("the demo seed must register machines")
	}
	vm01 := machines[0].ID
	capture("GET /api/admin/machines/{id}", e.asAdmin("GET", fmt.Sprintf("/api/admin/machines/%d", vm01), nil))

	// ---------------------------------------------------------------- admin writes (on throwaway records)

	var prod core.ProductView
	r = capture("POST /api/admin/products", e.asAdmin("POST", "/api/admin/products", map[string]any{
		"name": "Produk Kontrak", "description": "dipakai tes kontrak", "price": 7500}))
	e.mustOK(r, &prod)
	capture("PUT /api/admin/products/{id}", e.asAdmin("PUT", fmt.Sprintf("/api/admin/products/%d", prod.ID), map[string]any{
		"name": "Produk Kontrak", "description": "diubah", "price": 8000, "imageUrl": prod.ImageURL}))
	capture("POST /api/admin/products/{id}/archive", e.asAdmin("POST", fmt.Sprintf("/api/admin/products/%d/archive", prod.ID), nil))
	capture("POST /api/admin/products/{id}/restore", e.asAdmin("POST", fmt.Sprintf("/api/admin/products/%d/restore", prod.ID), nil))

	var newMachine core.MachineWithKey
	r = capture("POST /api/admin/machines", e.asAdmin("POST", "/api/admin/machines", map[string]any{
		"code": "VM-CT", "name": "Mesin Kontrak", "location": "Lab", "slotCount": 2, "capacity": 10}))
	e.mustOK(r, &newMachine)
	ctID, ctKey := newMachine.Machine.ID, newMachine.APIKey

	capture("PUT /api/admin/machines/{id}/slots/{n}", e.asAdmin("PUT", fmt.Sprintf("/api/admin/machines/%d/slots/1", ctID), map[string]any{
		"capacity": 12, "nextProductId": prod.ID}))
	capture("POST /api/admin/machines/{id}/deactivate", e.asAdmin("POST", fmt.Sprintf("/api/admin/machines/%d/deactivate", ctID), nil))
	capture("GET /api/machine/catalog (inactive machine)", e.asMachine(ctKey, "GET", "/api/machine/catalog", nil))
	capture("POST /api/admin/machines/{id}/activate", e.asAdmin("POST", fmt.Sprintf("/api/admin/machines/%d/activate", ctID), nil))
	r = capture("POST /api/admin/machines/{id}/rotate-key", e.asAdmin("POST", fmt.Sprintf("/api/admin/machines/%d/rotate-key", ctID), nil))
	e.mustOK(r, &newMachine)
	ctKey = newMachine.APIKey

	var opIDs map[string]int64
	r = capture("POST /api/admin/operators", e.asAdmin("POST", "/api/admin/operators", map[string]any{
		"name": "Operator Kontrak", "username": "op_kontrak", "password": "kontrak123"}))
	e.mustOK(r, &opIDs)
	opID := opIDs["id"]
	capture("POST /api/admin/operators/{id}/deactivate", e.asAdmin("POST", fmt.Sprintf("/api/admin/operators/%d/deactivate", opID), nil))
	capture("POST /api/admin/operators/{id}/activate", e.asAdmin("POST", fmt.Sprintf("/api/admin/operators/%d/activate", opID), nil))
	capture("POST /api/admin/operators/{id}/reset-password", e.asAdmin("POST", fmt.Sprintf("/api/admin/operators/%d/reset-password", opID), nil))

	// ---------------------------------------------------------------- kiosk: machine endpoints

	key := res.MachineKeys["VM-01"]
	if key == "" {
		t.Fatal("the demo seed must report VM-01's key")
	}
	capture("GET /api/machine/catalog", e.asMachine(key, "GET", "/api/machine/catalog", nil))
	capture("POST /api/machine/heartbeat", e.asMachine(key, "POST", "/api/machine/heartbeat", nil))

	// Operator mode (UC-04, UC-05).
	var opLogin core.LoginResult
	r = capture("POST /api/machine/operator/login", e.asMachine(key, "POST", "/api/machine/operator/login",
		map[string]string{"username": "budi", "password": "budi123"}))
	e.mustOK(r, &opLogin)
	capture("GET /api/machine/operator/slots", e.asOperator(key, opLogin.Token, "GET", "/api/machine/operator/slots", nil))
	capture("POST /api/machine/restock-sessions", e.asOperator(key, opLogin.Token, "POST", "/api/machine/restock-sessions", map[string]any{
		"id":    security.NewUUID(),
		"note":  "kunjungan kontrak",
		"items": []map[string]any{{"slotNumber": 1, "qtyAfter": 8}},
	}))
	capture("POST /api/machine/operator/logout", e.asOperator(key, opLogin.Token, "POST", "/api/machine/operator/logout", nil))

	// A whole purchase (UC-02, UC-03, FR-07, FR-09).
	cat := e.catalog(key)
	slot := firstAvailable(t, cat)
	txID := security.NewUUID()
	capture("POST /api/machine/transactions", e.asMachine(key, "POST", "/api/machine/transactions", map[string]any{
		"id": txID, "slotNumber": slot, "catalogVersion": cat.CatalogVersion}))
	capture("GET /api/machine/transactions/{id}", e.asMachine(key, "GET", "/api/machine/transactions/"+txID, nil))
	capture("POST /api/machine/transactions/{id}/pay", e.asMachine(key, "POST", "/api/machine/transactions/"+txID+"/pay", nil))
	capture("POST /api/machine/transactions/{id}/result", e.asMachine(key, "POST", "/api/machine/transactions/"+txID+"/result",
		map[string]string{"status": "DISPENSED"}))
	capture("POST /api/machine/transactions/{id}/report", e.asMachine(key, "POST", "/api/machine/transactions/"+txID+"/report", nil))

	// A cancelled purchase (FR-11).
	cancelID := e.startPurchase(key, slot)
	capture("POST /api/machine/transactions/{id}/cancel", e.asMachine(key, "POST", "/api/machine/transactions/"+cancelID+"/cancel",
		map[string]string{"reason": "CANCEL"}))

	// ---------------------------------------------------------------- the error contract

	capture("GET /api/nope", e.do("GET", "/api/nope", nil, nil))
	capture("DELETE /api/admin/products", e.do("DELETE", "/api/admin/products", nil, nil))
	capture("GET /api/admin/me (no token)", e.do("GET", "/api/admin/me", nil, nil))
	capture("POST /api/admin/login (wrong password)", e.do("POST", "/api/admin/login",
		map[string]string{"username": "admin", "password": "salah-sekali"}, nil))
	capture("POST /api/admin/login (operator account)", e.do("POST", "/api/admin/login",
		map[string]string{"username": "budi", "password": "budi123"}, nil))
	capture("GET /api/machine/catalog (bad key)", e.asMachine("tidak-dikenal", "GET", "/api/machine/catalog", nil))
	capture("GET /api/machine/operator/slots (no operator token)", e.asMachine(key, "GET", "/api/machine/operator/slots", nil))
	capture("POST /api/admin/products (empty)", e.asAdmin("POST", "/api/admin/products", map[string]any{}))
	capture("POST /api/admin/products (bad json)", e.do("POST", "/api/admin/products", "bukan objek",
		map[string]string{"Authorization": "Bearer " + e.admin}))
	capture("GET /api/admin/machines/{id} (unknown)", e.asAdmin("GET", "/api/admin/machines/999999", nil))
	capture("PUT /api/admin/products/{id} (unknown)", e.asAdmin("PUT", "/api/admin/products/999999",
		map[string]any{"name": "X", "price": 1000}))
	capture("POST /api/machine/transactions/{id}/pay (unknown)", e.asMachine(key, "POST", "/api/machine/transactions/"+security.NewUUID()+"/pay", nil))
	capture("POST /api/machine/transactions (bad uuid)", e.asMachine(key, "POST", "/api/machine/transactions",
		map[string]any{"id": "bukan-uuid", "slotNumber": slot, "catalogVersion": cat.CatalogVersion}))
	capture("POST /api/machine/transactions (stale catalog)", e.asMachine(key, "POST", "/api/machine/transactions",
		map[string]any{"id": security.NewUUID(), "slotNumber": slot, "catalogVersion": cat.CatalogVersion - 1}))

	// An empty slot on the throwaway machine is sold out.
	ctCat := e.catalog(ctKey)
	capture("POST /api/machine/transactions (sold out)", e.asMachine(ctKey, "POST", "/api/machine/transactions",
		map[string]any{"id": security.NewUUID(), "slotNumber": 2, "catalogVersion": ctCat.CatalogVersion}))

	// Result before paying, and a report on something not dispensed.
	pendingID := e.startPurchase(key, slot)
	capture("POST /api/machine/transactions/{id}/result (not paid)", e.asMachine(key, "POST", "/api/machine/transactions/"+pendingID+"/result",
		map[string]string{"status": "DISPENSED"}))
	capture("POST /api/machine/transactions/{id}/report (not reportable)", e.asMachine(key, "POST", "/api/machine/transactions/"+pendingID+"/report", nil))
	capture("POST /api/machine/transactions/{id}/result (bad status)", e.asMachine(key, "POST", "/api/machine/transactions/"+pendingID+"/result",
		map[string]string{"status": "MUNGKIN"}))

	// Paying a cancelled transaction, and recording a second, different result.
	capture("POST /api/machine/transactions/{id}/pay (cancelled)", e.asMachine(key, "POST", "/api/machine/transactions/"+cancelID+"/pay", nil))
	capture("POST /api/machine/transactions/{id}/result (already recorded)", e.asMachine(key, "POST", "/api/machine/transactions/"+txID+"/result",
		map[string]string{"status": "FAILED", "failureReason": "MOTOR_TIMEOUT"}))

	// A body over the 1 MB limit.
	capture("POST /api/machine/transactions (too large)", e.asMachine(key, "POST", "/api/machine/transactions",
		map[string]any{"id": security.NewUUID(), "slotNumber": slot, "pad": strings.Repeat("x", 1<<20+1024)}))

	// Five wrong passwords lock the throwaway operator (FR-13).
	var locked reply
	for i := 0; i < 5; i++ {
		locked = e.asMachine(key, "POST", "/api/machine/operator/login",
			map[string]string{"username": "op_kontrak", "password": "salah-sekali"})
	}
	capture("POST /api/machine/operator/login (locked)", locked)

	// The payment window closes (BR-06). This moves the clock, so it goes last.
	expiringID := e.startPurchase(key, slot)
	e.clock.Advance(61 * time.Second)
	capture("POST /api/machine/transactions/{id}/pay (expired)", e.asMachine(key, "POST", "/api/machine/transactions/"+expiringID+"/pay", nil))

	// The report button closes five minutes after the sale (FR-09).
	lateID := e.buy(key, slot)
	e.clock.Advance(6 * time.Minute)
	capture("POST /api/machine/transactions/{id}/report (window closed)", e.asMachine(key, "POST", "/api/machine/transactions/"+lateID+"/report", nil))

	// The demo reset ends every session, so it is the very last call.
	capture("POST /api/admin/demo/reset", e.asAdmin("POST", "/api/admin/demo/reset", nil))
	capture("GET /api/admin/me (after reset)", e.asAdmin("GET", "/api/admin/me", nil))

	// ---------------------------------------------------------------- compare

	got := "# The API contract, as field-path shapes. Written by TestAPIContract.\n" +
		"# Regenerate after an intended change: go test ./internal/httpapi -run TestAPIContract -update\n\n" +
		strings.Join(blocks, "\n")

	golden := filepath.Join("testdata", "contract.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote " + golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v\nrun: go test ./internal/httpapi -run TestAPIContract -update", err)
	}
	if string(want) != got {
		t.Errorf("the API contract changed:\n%s", diff(string(want), got))
	}
}

// firstAvailable returns a slot number the kiosk can sell right now.
func firstAvailable(t *testing.T, c core.Catalog) int {
	t.Helper()
	for _, s := range c.Slots {
		if s.Available {
			return s.SlotNumber
		}
	}
	t.Fatal("the demo seed must leave at least one slot available")
	return 0
}

// diff reports the first differing lines, with a little context.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	shown := 0
	for i := 0; i < len(w) || i < len(g); i++ {
		var lw, lg string
		if i < len(w) {
			lw = w[i]
		}
		if i < len(g) {
			lg = g[i]
		}
		if lw != lg {
			fmt.Fprintf(&b, "line %d:\n  want: %s\n  got:  %s\n", i+1, lw, lg)
			if shown++; shown == 25 {
				b.WriteString("  … more differences follow\n")
				break
			}
		}
	}
	return b.String()
}
