package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"vending/internal/security"
)

// MachineInput is the body of POST /admin/machines.
type MachineInput struct {
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Location  string      `json:"location"`
	SlotCount json.Number `json:"slotCount"`
	Capacity  json.Number `json:"capacity"`
}

// SlotInput is the body of PUT /admin/machines/{id}/slots/{n}.
type SlotInput struct {
	Capacity      json.Number  `json:"capacity"`
	NextProductID *json.Number `json:"nextProductId"` // null: no planned change
}

var machineCodePattern = regexp.MustCompile(`^[A-Z0-9-]{2,20}$`)

func intField(n json.Number) (int64, bool) {
	v, err := strconv.ParseInt(n.String(), 10, 64)
	return v, err == nil
}

// rawInt reads a JSON value that must be a whole number: 3 is accepted;
// "3", 2.5, null and a missing value are not.
func rawInt(raw json.RawMessage) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return v, err == nil
}

// ListMachines returns every machine with its slot and revenue figures (UC-08).
func (s *Service) ListMachines(ctx context.Context) ([]MachineView, error) {
	return s.machineViews(ctx, 0)
}

// GetMachine returns one machine with its slots.
func (s *Service) GetMachine(ctx context.Context, id int64) (MachineView, error) {
	views, err := s.machineViews(ctx, id)
	if err != nil {
		return MachineView{}, err
	}
	if len(views) == 0 {
		return MachineView{}, errNotFound("Mesin tidak ditemukan.")
	}
	v := views[0]
	if v.Slots, err = s.slotViews(ctx, s.db, id); err != nil {
		return MachineView{}, err
	}
	return v, nil
}

// machineViews builds the machine cards; onlyID 0 means all machines.
func (s *Service) machineViews(ctx context.Context, onlyID int64) ([]MachineView, error) {
	q := `SELECT id, code, name, location, is_active, catalog_version, created_at, last_seen_at FROM machines`
	var args []any
	if onlyID != 0 {
		q += ` WHERE id = ?`
		args = append(args, onlyID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY code`, args...)
	if err != nil {
		return nil, err
	}
	var out []MachineView
	index := map[int64]int{}
	for rows.Next() {
		var v MachineView
		var loc sql.NullString
		var created, seen sql.NullTime
		if err := rows.Scan(&v.ID, &v.Code, &v.Name, &loc, &v.IsActive, &v.CatalogVersion, &created, &seen); err != nil {
			rows.Close()
			return nil, err
		}
		v.Location = loc.String
		v.CreatedAt = ms(created.Time)
		v.LastSeenAt = nullMs(seen)
		index[v.ID] = len(out)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}

	filter, fargs := "", []any{}
	if onlyID != 0 {
		filter, fargs = " AND machine_id = ?", []any{onlyID}
	}

	// Slot figures: empty means count 0 or no product; blocked means a failed dispense (needs_check).
	rows, err = s.db.QueryContext(ctx, `SELECT machine_id, COUNT(*), SUM(qty = 0 OR product_id IS NULL), SUM(needs_check)
		FROM slots WHERE 1 = 1`+filter+` GROUP BY machine_id`, fargs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var n, empty, blocked int
		if err := rows.Scan(&id, &n, &empty, &blocked); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].SlotCount, out[i].EmptySlots, out[i].BlockedSlots = n, empty, blocked
		}
	}
	rows.Close()

	monthStart := startOfMonth(s.now().In(s.loc)).UTC()
	rows, err = s.db.QueryContext(ctx, `SELECT machine_id, SUM(price), COUNT(*) FROM transactions
		WHERE status = 'DISPENSED' AND created_at >= ?`+filter+` GROUP BY machine_id`, append([]any{monthStart}, fargs...)...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, rev, n int64
		if err := rows.Scan(&id, &rev, &n); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].MonthRevenue, out[i].MonthSales = rev, n
		}
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT machine_id, MAX(created_at) FROM transactions
		WHERE status = 'DISPENSED'`+filter+` GROUP BY machine_id`, fargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var last sql.NullTime
		if err := rows.Scan(&id, &last); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].LastSaleAt = nullMs(last)
		}
	}
	return out, rows.Err()
}

const slotSelect = `SELECT s.slot_number, s.capacity, s.qty, s.needs_check,
		p.id, p.name, p.description, p.price, p.image_url,
		n.id, n.name, n.description, n.price, n.image_url
	FROM slots s
	LEFT JOIN products p ON p.id = s.product_id
	LEFT JOIN products n ON n.id = s.next_product_id`

func scanSlot(r rowScanner) (SlotView, error) {
	var v SlotView
	var pID, nID, pPrice, nPrice sql.NullInt64
	var pName, pDesc, pImg, nName, nDesc, nImg sql.NullString
	if err := r.Scan(&v.SlotNumber, &v.Capacity, &v.Qty, &v.NeedsCheck,
		&pID, &pName, &pDesc, &pPrice, &pImg, &nID, &nName, &nDesc, &nPrice, &nImg); err != nil {
		return v, err
	}
	if pID.Valid {
		v.Product = &ProductBrief{ID: pID.Int64, Name: pName.String, Description: pDesc.String, Price: pPrice.Int64, ImageURL: nullString(pImg)}
	}
	if nID.Valid {
		v.NextProduct = &ProductBrief{ID: nID.Int64, Name: nName.String, Description: nDesc.String, Price: nPrice.Int64, ImageURL: nullString(nImg)}
	}
	return v, nil
}

func (s *Service) slotViews(ctx context.Context, q queryer, machineID int64) ([]SlotView, error) {
	rows, err := q.QueryContext(ctx, slotSelect+` WHERE s.machine_id = ? ORDER BY s.slot_number`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SlotView{}
	for rows.Next() {
		v, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateMachine registers a machine and its empty slots, and returns the
// machine key once; only its hash is stored (FR-21, TC-26).
func (s *Service) CreateMachine(ctx context.Context, actor Principal, in MachineInput) (MachineWithKey, error) {
	fe := fieldErrors{}
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	name := strings.TrimSpace(in.Name)
	location := strings.TrimSpace(in.Location)
	if !machineCodePattern.MatchString(code) {
		fe.add("code", "Kode 2–20 karakter: huruf, angka atau tanda hubung.")
	}
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe.add("name", "Nama wajib diisi.")
	case n > 100:
		fe.add("name", "Nama paling panjang 100 karakter.")
	}
	if utf8.RuneCountInString(location) > 150 {
		fe.add("location", "Lokasi paling panjang 150 karakter.")
	}
	slotCount, ok := intField(in.SlotCount)
	if !ok || slotCount < 1 || slotCount > 60 {
		fe.add("slotCount", "Jumlah slot 1 sampai 60.")
	}
	capacity, ok := intField(in.Capacity)
	if !ok || capacity < 1 || capacity > 30 {
		fe.add("capacity", "Kapasitas 1 sampai 30.")
	}
	if err := fe.err(); err != nil {
		return MachineWithKey{}, err
	}

	key := security.NewToken("vmk_")
	var id int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE code = ?`, code).Scan(&exists)
		if err != nil {
			return err
		}
		if exists > 0 {
			return fieldErrors{"code": "Kode " + code + " sudah dipakai."}.err()
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO machines (code, name, location, api_key_hash, catalog_version, is_active, created_at)
			VALUES (?, ?, ?, ?, 1, TRUE, ?)`, code, name, strOrNil(location), security.HashToken(key), s.now())
		if err != nil {
			if isMySQLError(err, mysqlDuplicateKey) {
				return fieldErrors{"code": "Kode " + code + " sudah dipakai."}.err()
			}
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		args := make([]any, 0, slotCount*3)
		for n := int64(1); n <= slotCount; n++ {
			args = append(args, id, n, capacity)
		}
		values := strings.TrimSuffix(strings.Repeat("(?, ?, ?, 0, FALSE),", int(slotCount)), ",")
		if _, err := tx.ExecContext(ctx, `INSERT INTO slots (machine_id, slot_number, capacity, qty, needs_check) VALUES `+values, args...); err != nil {
			return err
		}
		after := map[string]any{"code": code, "name": name, "slots": slotCount, "capacity": capacity}
		if location != "" {
			after["location"] = location
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionCreate, entityType: entityMachine, entityID: id, label: code + " " + name, after: after,
		})
	})
	if err != nil {
		return MachineWithKey{}, err
	}
	v, err := s.GetMachine(ctx, id)
	return MachineWithKey{Machine: v, APIKey: key}, err
}

// SetMachineActive deactivates or reactivates a machine. A deactivated
// machine's key is refused, so its kiosk stops selling (BR-12, TC-27).
func (s *Service) SetMachineActive(ctx context.Context, actor Principal, id int64, active bool) (MachineView, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var code, name string
		var cur bool
		err := tx.QueryRowContext(ctx, `SELECT code, name, is_active FROM machines WHERE id = ? FOR UPDATE`, id).Scan(&code, &name, &cur)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("Mesin tidak ditemukan.")
		}
		if err != nil || cur == active {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE machines SET is_active = ? WHERE id = ?`, active, id); err != nil {
			return err
		}
		if !active {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE machine_id = ?`, id); err != nil {
				return err
			}
		}
		action := actionUpdate
		if !active {
			action = actionDeactivate
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: action, entityType: entityMachine, entityID: id, label: code + " " + name,
			before: map[string]any{"isActive": !active}, after: map[string]any{"isActive": active},
		})
	})
	if err != nil {
		return MachineView{}, err
	}
	return s.GetMachine(ctx, id)
}

// RotateMachineKey replaces a machine's key, for when the old one is lost or
// leaked. The old key stops working at once; the new one is shown once.
func (s *Service) RotateMachineKey(ctx context.Context, actor Principal, id int64) (MachineWithKey, error) {
	key := security.NewToken("vmk_")
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var code, name string
		err := tx.QueryRowContext(ctx, `SELECT code, name FROM machines WHERE id = ? FOR UPDATE`, id).Scan(&code, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("Mesin tidak ditemukan.")
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE machines SET api_key_hash = ? WHERE id = ?`, security.HashToken(key), id); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionUpdate, entityType: entityMachine, entityID: id, label: code + " " + name,
			before: map[string]any{"apiKey": "(kunci lama)"}, after: map[string]any{"apiKey": "(kunci baru)"},
		})
	})
	if err != nil {
		return MachineWithKey{}, err
	}
	v, err := s.GetMachine(ctx, id)
	return MachineWithKey{Machine: v, APIKey: key}, err
}

// UpdateSlot changes a slot's capacity (1–30, never below its count) and its
// planned next product (FR-22). The plan takes effect only at the next visit,
// so the kiosk keeps showing the current product until then (BR-08, TC-18).
func (s *Service) UpdateSlot(ctx context.Context, actor Principal, machineID int64, slotNumber int, in SlotInput) (SlotView, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// Lock only the slot row; machine and product rows are read without locks so this
		// never waits in the opposite order to a purchase (machine, then slot).
		var slotID int64
		var capacity, qty int
		var cur, next sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT id, capacity, qty, product_id, next_product_id
			FROM slots WHERE machine_id = ? AND slot_number = ? FOR UPDATE`, machineID, slotNumber).
			Scan(&slotID, &capacity, &qty, &cur, &next)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("Slot tidak ditemukan.")
		}
		if err != nil {
			return err
		}
		var code string
		if err := tx.QueryRowContext(ctx, `SELECT code FROM machines WHERE id = ?`, machineID).Scan(&code); err != nil {
			return err
		}
		var nextName sql.NullString
		if next.Valid {
			if err := tx.QueryRowContext(ctx, `SELECT name FROM products WHERE id = ?`, next.Int64).Scan(&nextName); err != nil {
				return err
			}
		}

		fe := fieldErrors{}
		newCap, ok := intField(in.Capacity)
		switch {
		case !ok || newCap < 1 || newCap > 30:
			fe.add("capacity", "Kapasitas 1 sampai 30.")
		case int(newCap) < qty:
			fe.add("capacity", "Tidak boleh di bawah isi saat ini ("+strconv.Itoa(qty)+").")
		}
		var newNext sql.NullInt64
		var newNextName string
		if in.NextProductID != nil && in.NextProductID.String() != "" {
			pid, ok := intField(*in.NextProductID)
			var active bool
			if ok {
				// A shared lock: an archive of this product waits until this plan is saved, and then sees it.
				err = tx.QueryRowContext(ctx, `SELECT name, is_active FROM products WHERE id = ? LOCK IN SHARE MODE`, pid).Scan(&newNextName, &active)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
			}
			switch {
			case !ok || !active:
				fe.add("nextProductId", "Pilih produk yang aktif.")
			case cur.Valid && cur.Int64 == pid:
				// planning the product the coil already holds means no change
			default:
				newNext = sql.NullInt64{Int64: pid, Valid: true}
			}
		}
		if err := fe.err(); err != nil {
			return err
		}
		if !newNext.Valid {
			newNextName = ""
		}

		before := map[string]any{"capacity": int64(capacity), "nextProduct": nextNameOrNil(nextName)}
		after := map[string]any{"capacity": newCap, "nextProduct": strOrNil(newNextName)}
		b, a, changed := changedFields(before, after)
		if !changed {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE slots SET capacity = ?, next_product_id = ? WHERE id = ?`, newCap, newNext, slotID); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionUpdate, entityType: entitySlot, entityID: slotID,
			label: code + " · Slot " + strconv.Itoa(slotNumber), before: b, after: a,
		})
	})
	if err != nil {
		return SlotView{}, err
	}
	return scanSlot(s.db.QueryRowContext(ctx, slotSelect+` WHERE s.machine_id = ? AND s.slot_number = ?`, machineID, slotNumber))
}

func nextNameOrNil(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// RotateMachineKeyByCode is RotateMachineKey for the command line. The change
// log needs a person, so it is recorded under the first active admin.
func (s *Service) RotateMachineKeyByCode(ctx context.Context, code string) (MachineWithKey, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM machines WHERE code = ?`, strings.ToUpper(strings.TrimSpace(code))).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineWithKey{}, errNotFound("no machine with code " + code)
	}
	if err != nil {
		return MachineWithKey{}, err
	}
	var admin int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE role = 'ADMIN' AND is_active ORDER BY id LIMIT 1`).Scan(&admin)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineWithKey{}, errors.New("create an admin account first (create-admin): the change log records who rotated the key")
	}
	if err != nil {
		return MachineWithKey{}, err
	}
	return s.RotateMachineKey(ctx, Principal{UserID: admin}, id)
}
