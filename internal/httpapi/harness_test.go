package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-sql-driver/mysql"

	"vending/internal/config"
	"vending/internal/core"
	"vending/internal/database"
	"vending/internal/httpapi"
	"vending/internal/security"
)

// The integration tests run the real HTTP handlers against a real MySQL. Each
// test gets its own empty database, dropped afterwards. Set, for example:
//
//	VENDING_TEST_DSN='root:secret@tcp(127.0.0.1:3306)/' go test ./...

var jakarta, _ = time.LoadLocation("Asia/Jakarta")

// t0 is the tests' "now": Thursday 22 October 2026, 10:00 WIB.
var t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	svc   *core.Service
	clock *fakeClock
	admin string // admin session token
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("VENDING_TEST_DSN")
	if dsn == "" {
		t.Skip("set VENDING_TEST_DSN (e.g. root:secret@tcp(127.0.0.1:3306)/) to run the MySQL integration tests")
	}
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("VENDING_TEST_DSN: %v", err)
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	mc.DBName = fmt.Sprintf("vending_test_%d_%s", os.Getpid(), hex.EncodeToString(b[:]))
	dbc := config.DBConfig{DSN: mc.FormatDSN()}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.EnsureDatabase(ctx, dbc); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, dbc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP DATABASE `" + mc.DBName + "`")
		db.Close()
	})
	if _, err := database.Migrate(ctx, db, log); err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: t0}
	svc := core.New(db, core.Options{Location: jakarta, UploadDir: t.TempDir(), Now: clock.Now, Logger: log})
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>admin</title>")}}
	srv := httptest.NewServer(httpapi.New(svc, web, log))
	t.Cleanup(srv.Close)

	e := &env{t: t, srv: srv, svc: svc, clock: clock}
	if _, err := svc.CreateAdmin(ctx, "Admin Test", "admin", "admin12345"); err != nil {
		t.Fatal(err)
	}
	e.relogin()
	return e
}

// relogin signs the admin in again, after the clock jumped past the 30-minute idle limit.
func (e *env) relogin() {
	e.t.Helper()
	var login core.LoginResult
	e.mustOK(e.do("POST", "/api/admin/login", map[string]string{"username": "admin", "password": "admin12345"}, nil), &login)
	e.admin = login.Token
}

type reply struct {
	status int
	body   []byte
}

func (r reply) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(r.body, &m)
	return m
}

func (r reply) code() string { s, _ := r.json()["code"].(string); return s }

func (e *env) do(method, path string, body any, headers map[string]string) reply {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return reply{status: res.StatusCode, body: raw}
}

// mustOK fails the test unless the reply is 2xx, and decodes it into dst.
func (e *env) mustOK(r reply, dst any) {
	e.t.Helper()
	if r.status < 200 || r.status > 299 {
		e.t.Fatalf("want 2xx, got %d: %s", r.status, r.body)
	}
	if dst != nil {
		if err := json.Unmarshal(r.body, dst); err != nil {
			e.t.Fatalf("decode %s: %v", r.body, err)
		}
	}
}

// expect fails the test unless the reply has this status and error code.
func (e *env) expect(r reply, status int, code string) map[string]any {
	e.t.Helper()
	if r.status != status || (code != "" && r.code() != code) {
		e.t.Fatalf("want %d %s, got %d: %s", status, code, r.status, r.body)
	}
	return r.json()
}

func (e *env) asAdmin(method, path string, body any) reply {
	e.t.Helper()
	return e.do(method, path, body, map[string]string{"Authorization": "Bearer " + e.admin})
}

func (e *env) asMachine(key, method, path string, body any) reply {
	e.t.Helper()
	return e.do(method, path, body, map[string]string{"X-Machine-Key": key})
}

func (e *env) asOperator(key, token, method, path string, body any) reply {
	e.t.Helper()
	return e.do(method, path, body, map[string]string{"X-Machine-Key": key, "Authorization": "Bearer " + token})
}

// ---------------------------------------------------------------- scenario helpers

func (e *env) createProduct(name string, price int) int64 {
	e.t.Helper()
	var p core.ProductView
	e.mustOK(e.asAdmin("POST", "/api/admin/products", map[string]any{"name": name, "price": price}), &p)
	return p.ID
}

func (e *env) createMachine(code string, slots, capacity int) (int64, string) {
	e.t.Helper()
	var r core.MachineWithKey
	e.mustOK(e.asAdmin("POST", "/api/admin/machines", map[string]any{"code": code, "name": "Mesin " + code, "slotCount": slots, "capacity": capacity}), &r)
	return r.Machine.ID, r.APIKey
}

func (e *env) planSlot(machineID int64, slot int, productID int64, capacity int) {
	e.t.Helper()
	e.mustOK(e.asAdmin("PUT", fmt.Sprintf("/api/admin/machines/%d/slots/%d", machineID, slot), map[string]any{"capacity": capacity, "nextProductId": productID}), nil)
}

func (e *env) createOperator(username, password string) int64 {
	e.t.Helper()
	var r map[string]int64
	e.mustOK(e.asAdmin("POST", "/api/admin/operators", map[string]any{"name": "Operator " + username, "username": username, "password": password}), &r)
	return r["id"]
}

func (e *env) operatorLogin(key, username, password string) string {
	e.t.Helper()
	var r core.LoginResult
	e.mustOK(e.asMachine(key, "POST", "/api/machine/operator/login", map[string]string{"username": username, "password": password}), &r)
	return r.Token
}

// restock runs one visit that sets each listed slot to its count.
func (e *env) restock(key, token string, counts map[int]int) core.RestockSaved {
	e.t.Helper()
	items := []map[string]any{}
	for n, q := range counts {
		items = append(items, map[string]any{"slotNumber": n, "qtyAfter": q})
	}
	var r core.RestockSaved
	e.mustOK(e.asOperator(key, token, "POST", "/api/machine/restock-sessions", map[string]any{"id": security.NewUUID(), "items": items}), &r)
	return r
}

// setupMachine registers a machine whose slots hold the given products, filled by one visit.
func (e *env) setupMachine(code string, capacity int, products []int64, counts []int) (int64, string) {
	e.t.Helper()
	id, key := e.createMachine(code, len(products), capacity)
	for i, p := range products {
		e.planSlot(id, i+1, p, capacity)
	}
	e.createOperator("op_"+lower(code), "operator123")
	tok := e.operatorLogin(key, "op_"+lower(code), "operator123")
	m := map[int]int{}
	for i, q := range counts {
		m[i+1] = q
	}
	e.restock(key, tok, m)
	e.mustOK(e.asOperator(key, tok, "POST", "/api/machine/operator/logout", nil), nil)
	return id, key
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z':
			b[i] = c + 32
		case c == '-':
			b[i] = '_'
		}
	}
	return string(b)
}

func (e *env) catalog(key string) core.Catalog {
	e.t.Helper()
	var c core.Catalog
	e.mustOK(e.asMachine(key, "GET", "/api/machine/catalog", nil), &c)
	return c
}

// startPurchase creates a PENDING transaction for a slot against the current catalog.
func (e *env) startPurchase(key string, slot int) string {
	e.t.Helper()
	id := security.NewUUID()
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions", map[string]any{"id": id, "slotNumber": slot, "catalogVersion": e.catalog(key).CatalogVersion}), nil)
	return id
}

// buy runs a whole purchase: create, pay, and the agent's DISPENSED report.
func (e *env) buy(key string, slot int) string {
	e.t.Helper()
	id := e.startPurchase(key, slot)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/pay", nil), nil)
	e.mustOK(e.asMachine(key, "POST", "/api/machine/transactions/"+id+"/result", map[string]string{"status": "DISPENSED"}), nil)
	return id
}

func (e *env) machineView(id int64) core.MachineView {
	e.t.Helper()
	var m core.MachineView
	e.mustOK(e.asAdmin("GET", fmt.Sprintf("/api/admin/machines/%d", id), nil), &m)
	return m
}

func (e *env) slotQty(machineID int64, slot int) int {
	e.t.Helper()
	return e.machineView(machineID).Slots[slot-1].Qty
}

func (e *env) transactions(query string) core.TransactionPage {
	e.t.Helper()
	var p core.TransactionPage
	e.mustOK(e.asAdmin("GET", "/api/admin/transactions?"+query, nil), &p)
	return p
}
