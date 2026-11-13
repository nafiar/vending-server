package api_test

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

	"vending/internal/app"
	"vending/internal/common/config"
	"vending/internal/common/security"
	deliveryhttp "vending/internal/delivery/http"
	"vending/internal/repository/mysqldb"
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
	t   *testing.T
	srv *httptest.Server
	// deps is the graph, for the few things no request triggers: the
	// maintenance sweeps, the bookkeeping check and the demo load.
	deps  app.Deps
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
	if err := mysqldb.EnsureDatabase(ctx, dbc); err != nil {
		t.Fatal(err)
	}
	db, err := mysqldb.Open(ctx, dbc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP DATABASE `" + mc.DBName + "`")
		db.Close()
	})
	if _, err := mysqldb.Migrate(ctx, db, log); err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: t0}
	uploads := t.TempDir()
	// The graph is built by the same function the binary uses, so these tests
	// exercise the wiring that ships.
	// Start from the shipped defaults and change only what the tests need, so
	// the system under test runs with the timings that ship (BR-06, BR-13).
	cfg := config.Defaults()
	cfg.Location, cfg.UploadDir = jakarta, uploads
	deps := app.Wire(db, cfg, log, clock.Now)
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>admin</title>")}}
	srv := httptest.NewServer(deliveryhttp.New(deps.UseCases(), web, log, deliveryhttp.Options{
		UploadDir: uploads,
		DemoReset: func(ctx context.Context) error { _, err := deps.Demo.Load(ctx, true); return err },
	}))
	t.Cleanup(srv.Close)

	e := &env{t: t, srv: srv, deps: deps, clock: clock}
	if _, err := deps.OperatorConsole.CreateAdmin(ctx, "Admin Test", "admin", "admin12345"); err != nil {
		t.Fatal(err)
	}
	e.relogin()
	return e
}

// relogin signs the admin in again, after the clock jumped past the 30-minute idle limit.
func (e *env) relogin() {
	e.t.Helper()
	var login loginReply
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
	var p productReply
	e.mustOK(e.asAdmin("POST", "/api/admin/products", map[string]any{"name": name, "price": price}), &p)
	return p.ID
}

func (e *env) createMachine(code string, slots, capacity int) (int64, string) {
	e.t.Helper()
	var r machineKeyReply
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
	var r loginReply
	e.mustOK(e.asMachine(key, "POST", "/api/machine/operator/login", map[string]string{"username": username, "password": password}), &r)
	return r.Token
}

// restock runs one visit that sets each listed slot to its count.
func (e *env) restock(key, token string, counts map[int]int) savedReply {
	e.t.Helper()
	items := []map[string]any{}
	for n, q := range counts {
		items = append(items, map[string]any{"slotNumber": n, "qtyAfter": q})
	}
	var r savedReply
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

// catalogReply is the catalog as the kiosk reads it. The tests declare the
// shape they expect instead of importing a production type, so moving a type
// between layers cannot quietly change what they assert.
type catalogReply struct {
	CatalogVersion int64 `json:"catalogVersion"`
	Machine        struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"machine"`
	Slots []struct {
		SlotNumber int  `json:"slotNumber"`
		Available  bool `json:"available"`
		Product    *struct {
			ID          int64   `json:"id"`
			Name        string  `json:"name"`
			Description string  `json:"description"`
			Price       int64   `json:"price"`
			ImageURL    *string `json:"imageUrl"`
		} `json:"product"`
	} `json:"slots"`
}

// heartbeatReply is what the agent reads from a heartbeat.
type heartbeatReply struct {
	OK             bool  `json:"ok"`
	ServerTime     int64 `json:"serverTime"`
	CatalogVersion int64 `json:"catalogVersion"`
}

func (e *env) catalog(key string) catalogReply {
	e.t.Helper()
	var c catalogReply
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

func (e *env) machineView(id int64) machineReply {
	e.t.Helper()
	var m machineReply
	e.mustOK(e.asAdmin("GET", fmt.Sprintf("/api/admin/machines/%d", id), nil), &m)
	return m
}

func (e *env) slotQty(machineID int64, slot int) int {
	e.t.Helper()
	return e.machineView(machineID).Slots[slot-1].Qty
}

func (e *env) transactions(query string) transactionPage {
	e.t.Helper()
	var p transactionPage
	e.mustOK(e.asAdmin("GET", "/api/admin/transactions?"+query, nil), &p)
	return p
}

// createdReply is a purchase as the kiosk reads it back, and stateReply and
// reportReply are the answers to the steps after it. Like catalogReply, these
// are the tests' own declaration of the contract: an end-to-end test that
// unmarshalled into a production type would keep passing while the wire shape
// moved under it.
type createdReply struct {
	ID         string `json:"id"`
	SlotNumber int    `json:"slotNumber"`
	Product    struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Price       int64   `json:"price"`
		ImageURL    *string `json:"imageUrl"`
	} `json:"product"`
	Amount           int64  `json:"amount"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
	Status           string `json:"status"`
	CreatedAt        int64  `json:"createdAt"`
}

type stateReply struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	FailureReason      *string `json:"failureReason"`
	PaidAt             *int64  `json:"paidAt"`
	FinishedAt         *int64  `json:"finishedAt"`
	CustomerReportedAt *int64  `json:"customerReportedAt"`
}

type reportReply struct {
	ID         string `json:"id"`
	Reported   bool   `json:"reported"`
	ReportedAt int64  `json:"reportedAt"`
}

// loginReply and meReply are what the admin web and the kiosk's operator
// screen read when someone signs in, declared here rather than imported, for
// the reason catalogReply gives.
type loginReply struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type meReply struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Role     string `json:"role"`
	DemoMode bool   `json:"demoMode"`
}

// savedReply and operatorSlotsReply are what the kiosk's restock screen reads,
// declared here rather than imported, for the reason catalogReply gives.
type savedReply struct {
	ID             string `json:"id"`
	SessionID      int64  `json:"sessionId"`
	SavedAt        int64  `json:"savedAt"`
	CatalogVersion int64  `json:"catalogVersion"`
}

type slotReply struct {
	SlotNumber int  `json:"slotNumber"`
	Capacity   int  `json:"capacity"`
	Qty        int  `json:"qty"`
	NeedsCheck bool `json:"needsCheck"`
	Product    *struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Price int64  `json:"price"`
	} `json:"product"`
	NextProduct *struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Price int64  `json:"price"`
	} `json:"nextProduct"`
}

type operatorSlotsReply struct {
	Machine struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"machine"`
	Slots []slotReply `json:"slots"`
}

// productReply, machineReply and machineKeyReply are what the admin web reads,
// declared here rather than imported, for the reason catalogReply gives.
type productReply struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       int64   `json:"price"`
	ImageURL    *string `json:"imageUrl"`
	IsActive    bool    `json:"isActive"`
	CreatedAt   int64   `json:"createdAt"`
	UpdatedAt   int64   `json:"updatedAt"`
	UsedIn      []struct {
		MachineCode string `json:"machineCode"`
		SlotNumber  int    `json:"slotNumber"`
		Planned     bool   `json:"planned"`
	} `json:"usedIn"`
}

type machineReply struct {
	ID             int64       `json:"id"`
	Code           string      `json:"code"`
	Name           string      `json:"name"`
	Location       string      `json:"location"`
	IsActive       bool        `json:"isActive"`
	CatalogVersion int64       `json:"catalogVersion"`
	CreatedAt      int64       `json:"createdAt"`
	LastSeenAt     *int64      `json:"lastSeenAt"`
	LastSaleAt     *int64      `json:"lastSaleAt"`
	SlotCount      int         `json:"slotCount"`
	EmptySlots     int         `json:"emptySlots"`
	BlockedSlots   int         `json:"blockedSlots"`
	MonthRevenue   int64       `json:"monthRevenue"`
	MonthSales     int64       `json:"monthSales"`
	Slots          []slotReply `json:"slots"`
}

type machineKeyReply struct {
	Machine machineReply `json:"machine"`
	APIKey  string       `json:"apiKey"`
}

// operatorReply is a staff account as the admin's list shows it, declared here
// rather than imported, for the reason catalogReply gives.
type operatorReply struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Username         string  `json:"username"`
	Role             string  `json:"role"`
	IsActive         bool    `json:"isActive"`
	CreatedAt        int64   `json:"createdAt"`
	LastVisitAt      *int64  `json:"lastVisitAt"`
	LastVisitMachine *string `json:"lastVisitMachine"`
	Visits30         int64   `json:"visits30"`
}

// recapReply and summaryReply are the revenue figures as the admin web reads
// them, declared here rather than imported, for the reason catalogReply gives.
type totalsReply struct {
	Revenue int64 `json:"revenue"`
	Sales   int64 `json:"sales"`
}

type summaryReply struct {
	Today             totalsReply `json:"today"`
	SameTimeYesterday totalsReply `json:"sameTimeYesterday"`
	Last7             totalsReply `json:"last7"`
	Month             totalsReply `json:"month"`
}

type breakdownReply struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Revenue int64  `json:"revenue"`
	Sales   int64  `json:"sales"`
}

type recapReply struct {
	From      string           `json:"from"`
	To        string           `json:"to"`
	GroupBy   string           `json:"groupBy"`
	Breakdown []breakdownReply `json:"breakdown"`
	Totals    struct {
		Revenue   int64 `json:"revenue"`
		Sales     int64 `json:"sales"`
		Failed    int64 `json:"failed"`
		Reported  int64 `json:"reported"`
		Cancelled int64 `json:"cancelled"`
	} `json:"totals"`
	Series []struct {
		Period  string `json:"period"`
		Start   int64  `json:"start"`
		Revenue int64  `json:"revenue"`
		Sales   int64  `json:"sales"`
	} `json:"series"`
	ByMachine []struct {
		MachineID  int64  `json:"machineId"`
		Code       string `json:"code"`
		Name       string `json:"name"`
		Location   string `json:"location"`
		IsActive   bool   `json:"isActive"`
		Revenue    int64  `json:"revenue"`
		Sales      int64  `json:"sales"`
		LastSaleAt *int64 `json:"lastSaleAt"`
		LastSeenAt *int64 `json:"lastSeenAt"`
	} `json:"byMachine"`
	TopProducts []breakdownReply `json:"topProducts"`
}
