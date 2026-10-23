// Package seed loads demo data: three machines with about 90 days of sales and
// restock visits, the same story as the admin web's mock mode, but simulated
// event by event so every count reconciles with its history (NFR-05).
package seed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"vending/internal/core"
	"vending/internal/security"
)

// Result is what the seed created, for the command to print.
type Result struct {
	Users        []string          // "username / password"
	MachineKeys  map[string]string // machine code -> key; empty when a reset kept the machine's old key
	Transactions int
	Visits       int
}

type product struct {
	id         int64
	name, desc string
	price      int64
	createdAgo int // days
	active     bool
	image      string
}

type machine struct {
	id          int64
	code, name  string
	location    string
	layout      []int64 // product id per slot
	createdAgo  int
	seenAgo     time.Duration
	dailySales  int
	created     time.Time
	lastSeen    time.Time
	version     int64
	slots       []*slot
	apiKey      string
	keyHash     string
	lastVisitAt time.Time
}

type slot struct {
	id       int64
	number   int
	product  int64
	next     int64
	capacity int
	qty      int
	blocked  bool
}

type event struct {
	at    time.Time
	visit bool
	end   time.Time // visits only
	last  bool      // the machine's last visit
}

type txRow struct {
	id                          string
	machineID, slotID, product  int64
	price                       int64
	status, reason              string
	created, paid, finished, rp time.Time
}

type visitRow struct {
	id                int64
	clientID          string
	machineID, userID int64
	started, finished time.Time
	items             []itemRow
}

type itemRow struct {
	slotID, product int64
	before, after   int
	productChanged  bool
}

type auditRow struct {
	action, entity string
	entityID       int64
	label          string
	before, after  map[string]any
	at             time.Time
}

var popularity = map[int64]float64{1: 0.7, 2: 0.9, 3: 1.2, 4: 1.4, 5: 1.8, 6: 1.0, 7: 0.8, 8: 1.3, 9: 0.6}
var saleHours = []int{7, 8, 9, 9, 10, 11, 12, 12, 12, 13, 13, 14, 15, 15, 16, 17, 17, 18, 19}

// Demo fills an empty database. With reset it first deletes every row, but
// keeps the keys of machines that come back with the same code (VM-01 to
// VM-03), so a kiosk already set up with its key keeps working after a reset.
func Demo(ctx context.Context, svc *core.Service, reset bool) (Result, error) {
	db := svc.DB()
	keptKeys := map[string]string{}
	if reset {
		rows, err := db.QueryContext(ctx, `SELECT code, api_key_hash FROM machines`)
		if err != nil {
			return Result{}, err
		}
		for rows.Next() {
			var code, hash string
			if err := rows.Scan(&code, &hash); err != nil {
				rows.Close()
				return Result{}, err
			}
			keptKeys[code] = hash
		}
		rows.Close()
		if err := wipe(ctx, db); err != nil {
			return Result{}, err
		}
	} else {
		var n int
		err := db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users) + (SELECT COUNT(*) FROM machines) + (SELECT COUNT(*) FROM products)`).Scan(&n)
		if err != nil {
			return Result{}, err
		}
		if n > 0 {
			return Result{}, errors.New("the database already has data; run seed-demo -reset to delete it first, or use an empty database")
		}
	}

	loc := svc.Location()
	rng := rand.New(rand.NewPCG(20261013, 22))
	now := svc.Now()
	today := startOfDay(now.In(loc))
	at := func(daysAgo int, hour, minute int) time.Time {
		d := today.AddDate(0, 0, -daysAgo)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, loc).UTC()
	}
	priceChangeAt := at(20, 10, 0) // Gacha Ultra Rare: Rp30.000 -> Rp35.000
	andiOffAt := at(15, 11, 0)

	// ---- products and their images
	products := []*product{
		{1, "Gacha Legendary", "Satu pack berisi 5 kartu, peluang kartu legendaris.", 50000, 110, true, ""},
		{2, "Gacha Ultra Rare", "Satu pack berisi 5 kartu, minimal 1 ultra rare.", 35000, 110, true, ""},
		{3, "Gacha Rare", "Satu pack berisi 5 kartu, minimal 1 rare.", 25000, 110, true, ""},
		{4, "Gacha Uncommon", "Satu pack berisi 5 kartu uncommon.", 15000, 110, true, ""},
		{5, "Gacha Common", "Satu pack berisi 5 kartu common.", 10000, 110, true, ""},
		{6, "Gacha Mystery Box", "Isi acak dari semua tingkat.", 30000, 110, true, ""},
		{7, "Gacha Promo Edition", "Edisi promo terbatas.", 40000, 6, true, ""},
		{8, "Gacha Starter Pack", "Paket pemula, 3 kartu dan 1 sleeve.", 20000, 62, true, ""},
		{9, "Gacha Holiday 2025", "Edisi liburan akhir tahun.", 45000, 100, false, ""},
	}
	for _, p := range products {
		url, err := svc.StoreImage(core.PlaceholderSVG(p.name, int(p.id)), ".svg")
		if err != nil {
			return Result{}, err
		}
		p.image = url
	}
	priceAt := func(pid int64, t time.Time) int64 {
		if pid == 2 && t.Before(priceChangeAt) {
			return 30000
		}
		return products[pid-1].price
	}

	// ---- machines and slots
	machines := []*machine{
		{id: 1, code: "VM-01", name: "Lobby Gedung A", location: "Gedung A, lantai 1", layout: []int64{1, 2, 3, 4, 5, 6}, createdAgo: 95, seenAgo: 2 * time.Minute, dailySales: 12},
		{id: 2, code: "VM-02", name: "Kantin Utama", location: "Kantin, lantai dasar", layout: []int64{1, 2, 3, 4, 5, 6, 8, 5}, createdAgo: 60, seenAgo: time.Minute, dailySales: 18},
		{id: 3, code: "VM-03", name: "Perpustakaan", location: "Perpustakaan, lantai 2", layout: []int64{8, 5, 4, 3, 6, 2}, createdAgo: 30, seenAgo: 190 * time.Minute, dailySales: 7},
	}
	var slotID int64
	for _, m := range machines {
		m.created = at(m.createdAgo, 8, 0)
		m.lastSeen = now.Add(-m.seenAgo)
		m.version = 1
		if hash, ok := keptKeys[m.code]; ok {
			m.keyHash = hash
		} else {
			m.apiKey = security.NewToken("vmk_")
			m.keyHash = security.HashToken(m.apiKey)
		}
		for i, pid := range m.layout {
			slotID++
			m.slots = append(m.slots, &slot{id: slotID, number: i + 1, product: pid, capacity: 10})
		}
	}

	// ---- simulate each machine day by day: visits at 09:00 and 14:00 (Sunday 10:00), sales in between
	var txs []txRow
	var visits []visitRow
	var visitID int64
	for _, m := range machines {
		stop := m.lastSeen.Add(-time.Minute) // nothing happens after the machine's last contact
		var events []event
		events = append(events, event{at: m.created.Add(30 * time.Minute), visit: true}) // first fill
		for d := m.createdAgo; d >= 0; d-- {
			day := today.AddDate(0, 0, -d)
			if d < m.createdAgo {
				hours := []int{9, 14}
				if day.Weekday() == time.Sunday {
					hours = []int{10}
				}
				for _, h := range hours {
					events = append(events, event{at: at(d, h, rng.IntN(40)), visit: true})
				}
			}
			factor := 1.0
			if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
				factor = 0.45
			}
			n := int(math.Round(float64(m.dailySales) * factor * (0.7 + rng.Float64()*0.6)))
			for i := 0; i < n; i++ {
				t := at(d, saleHours[rng.IntN(len(saleHours))], rng.IntN(60)).Add(time.Duration(rng.IntN(60)) * time.Second)
				events = append(events, event{at: t})
			}
		}
		// keep events up to the last contact, give each visit its length, mark the last visit
		kept := events[:0]
		for _, e := range events {
			if e.at.Before(m.created) || !e.at.Before(stop) || e.at.After(now.Add(-5*time.Minute)) {
				continue
			}
			if e.visit {
				e.end = e.at.Add(time.Duration(4+rng.IntN(9)) * time.Minute)
				if e.end.After(stop) {
					continue
				}
			}
			kept = append(kept, e)
		}
		events = kept
		sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].visit {
				events[i].last = true
				break
			}
		}

		var busyUntil time.Time
		first := true
		for _, e := range events {
			if e.visit {
				visitID++
				operator := int64(2) // budi
				switch {
				case first:
				case e.at.Before(andiOffAt) && rng.Float64() < 0.25:
					operator = 4 // andi, before he was deactivated
				case rng.Float64() >= 0.55:
					operator = 3 // siti
				}
				v := visitRow{id: visitID, clientID: security.NewUUID(), machineID: m.id, userID: operator, started: e.at, finished: e.end}
				for _, s := range m.slots {
					before := s.qty
					after := s.capacity
					if s.product == 1 {
						after = max(before, 3+rng.IntN(3)) // the expensive tier is held low on purpose
					}
					if !first && rng.Float64() < 0.15 {
						after = before
					}
					if e.last && m.code == "VM-01" && s.number == 4 {
						after = 0 // the warehouse ran out of Uncommon: an empty slot to show "Habis"
					}
					v.items = append(v.items, itemRow{slotID: s.id, product: s.product, before: before, after: after, productChanged: first})
					s.qty, s.blocked = after, false
				}
				visits = append(visits, v)
				m.version++
				m.lastVisitAt = e.end
				busyUntil = e.end
				first = false
				continue
			}
			if e.at.Before(busyUntil) {
				continue // the kiosk doesn't sell during operator mode
			}
			s := pickSlot(rng, m.slots)
			if s == nil {
				continue // everything is sold out or blocked
			}
			tx := txRow{id: security.NewUUID(), machineID: m.id, slotID: s.id, product: s.product, price: priceAt(s.product, e.at), created: e.at}
			switch r := rng.Float64(); {
			case r < 0.915:
				tx.status, tx.paid, tx.finished = "DISPENSED", e.at.Add(12*time.Second), e.at.Add(time.Duration(15+rng.IntN(30))*time.Second)
				if rng.Float64() < 0.012 {
					tx.rp = tx.finished.Add(20 * time.Second)
				}
				s.qty--
			case r < 0.955:
				tx.status, tx.finished = "CANCELLED", e.at.Add(time.Duration(5+rng.IntN(25))*time.Second)
			case r < 0.985:
				tx.status, tx.finished = "EXPIRED", e.at.Add(60*time.Second)
			default:
				tx.status, tx.paid, tx.finished = "FAILED", e.at.Add(12*time.Second), e.at.Add(20*time.Second)
				tx.reason = "MOTOR_TIMEOUT"
				if rng.Float64() < 0.3 {
					tx.reason = "NO_ACK"
				}
				s.blocked = true
				m.version++
			}
			txs = append(txs, tx)
		}
		if priceChangeAt.After(m.created) {
			m.version++ // the price change raised the version of every machine selling Ultra Rare
		}
	}

	// One recent failed dispense on VM-02 slot 6, so the demo shows a blocked slot.
	vm2 := machines[1]
	if s := vm2.slots[5]; !s.blocked && s.qty > 0 {
		t := now.Add(-20 * time.Minute)
		if t.After(vm2.lastVisitAt) {
			txs = append(txs, txRow{id: security.NewUUID(), machineID: vm2.id, slotID: s.id, product: s.product, price: priceAt(s.product, t),
				status: "FAILED", reason: "MOTOR_TIMEOUT", created: t, paid: t.Add(12 * time.Second), finished: t.Add(20 * time.Second)})
			s.blocked = true
			vm2.version++
		}
	}
	machines[0].slots[2].next = 7 // VM-01 slot 3: Promo Edition planned for the next visit

	// ---- users
	type user struct {
		id                       int64
		name, username, password string
		role                     string
		active                   bool
		createdAgo               int
	}
	users := []user{
		{1, "Admin Demo", "admin", "admin123", "ADMIN", true, 120},
		{2, "Budi Santoso", "budi", "budi123", "OPERATOR", true, 100},
		{3, "Siti Rahma", "siti", "siti123", "OPERATOR", true, 70},
		{4, "Andi Pratama", "andi", "andi123", "OPERATOR", false, 99},
	}

	// ---- change log
	var audits []auditRow
	for _, p := range products {
		audits = append(audits, auditRow{"CREATE", "PRODUCT", p.id, p.name, nil,
			map[string]any{"name": p.name, "price": priceAt(p.id, at(p.createdAgo, 9, 0))}, at(p.createdAgo, 9, 0)})
	}
	for _, m := range machines {
		audits = append(audits, auditRow{"CREATE", "MACHINE", m.id, m.code + " " + m.name, nil,
			map[string]any{"code": m.code, "name": m.name, "location": m.location, "slots": len(m.slots), "capacity": 10}, m.created})
	}
	for _, u := range users[1:] {
		audits = append(audits, auditRow{"CREATE", "USER", u.id, u.name + " (" + u.username + ")", nil,
			map[string]any{"name": u.name, "username": u.username, "role": u.role}, at(u.createdAgo, 10, 0)})
	}
	audits = append(audits,
		auditRow{"UPDATE", "PRODUCT", 2, "Gacha Ultra Rare", map[string]any{"price": 30000}, map[string]any{"price": 35000}, priceChangeAt},
		auditRow{"DEACTIVATE", "USER", 4, "Andi Pratama (andi)", map[string]any{"isActive": true}, map[string]any{"isActive": false}, andiOffAt},
		auditRow{"ARCHIVE", "PRODUCT", 9, "Gacha Holiday 2025", map[string]any{"isActive": true}, map[string]any{"isActive": false}, at(10, 15, 0)},
		auditRow{"UPDATE", "SLOT", machines[0].slots[2].id, "VM-01 · Slot 3", map[string]any{"nextProduct": nil}, map[string]any{"nextProduct": "Gacha Promo Edition"}, at(2, 16, 0)},
	)

	// ---- write everything in one transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	res := Result{MachineKeys: map[string]string{}, Transactions: len(txs), Visits: len(visits)}
	for _, u := range users {
		hash, err := security.HashPassword(u.password)
		if err != nil {
			return Result{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, name, username, password_hash, role, is_active, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			u.id, u.name, u.username, hash, u.role, u.active, at(u.createdAgo, 10, 0)); err != nil {
			return Result{}, err
		}
		label := u.username + " / " + u.password + " (" + strings.ToLower(u.role)
		if !u.active {
			label += ", inactive"
		}
		res.Users = append(res.Users, label+")")
	}
	for _, p := range products {
		created := at(p.createdAgo, 9, 0)
		updated := created
		if p.id == 2 {
			updated = priceChangeAt
		}
		if !p.active {
			updated = at(10, 15, 0)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO products (id, name, description, price, image_url, is_active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.id, p.name, p.desc, p.price, p.image, p.active, created, updated); err != nil {
			return Result{}, err
		}
	}
	for _, m := range machines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO machines (id, code, name, location, api_key_hash, catalog_version, is_active, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, TRUE, ?, ?)`, m.id, m.code, m.name, m.location, m.keyHash, m.version, m.lastSeen, m.created); err != nil {
			return Result{}, err
		}
		res.MachineKeys[m.code] = m.apiKey
		for _, s := range m.slots {
			var next any
			if s.next != 0 {
				next = s.next
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO slots (id, machine_id, slot_number, product_id, next_product_id, capacity, qty, needs_check)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, s.id, m.id, s.number, s.product, next, s.capacity, s.qty, s.blocked); err != nil {
				return Result{}, err
			}
		}
	}
	if err := insertVisits(ctx, tx, visits); err != nil {
		return Result{}, err
	}
	if err := insertTransactions(ctx, tx, txs); err != nil {
		return Result{}, err
	}
	for _, a := range audits {
		var before, after any
		if a.before != nil {
			b, _ := json.Marshal(a.before)
			before = string(b)
		}
		b, _ := json.Marshal(a.after)
		after = string(b)
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (user_id, action, entity_type, entity_id, entity_label, before_json, after_json, created_at)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?)`, a.action, a.entity, a.entityID, a.label, before, after, a.at); err != nil {
			return Result{}, err
		}
	}
	return res, tx.Commit()
}

func pickSlot(rng *rand.Rand, slots []*slot) *slot {
	var total float64
	for _, s := range slots {
		if s.qty > 0 && !s.blocked {
			total += popularity[s.product]
		}
	}
	if total == 0 {
		return nil
	}
	pick := rng.Float64() * total
	for _, s := range slots {
		if s.qty > 0 && !s.blocked {
			pick -= popularity[s.product]
			if pick <= 0 {
				return s
			}
		}
	}
	for i := len(slots) - 1; i >= 0; i-- {
		if slots[i].qty > 0 && !slots[i].blocked {
			return slots[i]
		}
	}
	return nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func insertTransactions(ctx context.Context, tx *sql.Tx, rows []txRow) error {
	const batch = 500
	for start := 0; start < len(rows); start += batch {
		chunk := rows[start:min(start+batch, len(rows))]
		args := make([]any, 0, len(chunk)*11)
		for _, r := range chunk {
			var reason any
			if r.reason != "" {
				reason = r.reason
			}
			args = append(args, r.id, r.machineID, r.slotID, r.product, r.price, r.status, reason, nullTime(r.rp), r.created, nullTime(r.paid), nullTime(r.finished))
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),", len(chunk)), ",")
		if _, err := tx.ExecContext(ctx, `INSERT INTO transactions
			(id, machine_id, slot_id, product_id, price, status, failure_reason, customer_reported_at, created_at, paid_at, finished_at)
			VALUES `+values, args...); err != nil {
			return fmt.Errorf("insert transactions: %w", err)
		}
	}
	return nil
}

func insertVisits(ctx context.Context, tx *sql.Tx, visits []visitRow) error {
	const batch = 200
	for start := 0; start < len(visits); start += batch {
		chunk := visits[start:min(start+batch, len(visits))]
		args := make([]any, 0, len(chunk)*6)
		var items []any
		nItems := 0
		for _, v := range chunk {
			args = append(args, v.id, v.clientID, v.machineID, v.userID, v.started, v.finished)
			for _, it := range v.items {
				items = append(items, v.id, it.slotID, it.product, it.before, it.after, it.productChanged)
				nItems++
			}
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?),", len(chunk)), ",")
		if _, err := tx.ExecContext(ctx, `INSERT INTO restock_sessions (id, client_id, machine_id, user_id, started_at, finished_at) VALUES `+values, args...); err != nil {
			return fmt.Errorf("insert restock sessions: %w", err)
		}
		ivalues := strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?),", nItems), ",")
		if _, err := tx.ExecContext(ctx, `INSERT INTO restock_items (session_id, slot_id, product_id, qty_before, qty_after, product_changed) VALUES `+ivalues, items...); err != nil {
			return fmt.Errorf("insert restock items: %w", err)
		}
	}
	return nil
}

// wipe deletes every row, child tables first, on one connection so the
// foreign-key switch applies to all the statements.
func wipe(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS = 1`) //nolint:errcheck
	for _, t := range []string{"sessions", "audit_logs", "restock_items", "restock_sessions", "transactions", "slots", "machines", "products", "users"} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE "+t); err != nil {
			return fmt.Errorf("empty %s: %w", t, err)
		}
	}
	return nil
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
