package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------- admin: restock history (UC-10)

// RestockQuery filters GET /admin/restock-sessions.
type RestockQuery struct {
	From, To  string
	MachineID int64
	UserID    int64
	Page      int
	PageSize  int
	All       bool
}

// ListRestockSessions returns one row per operator visit, newest first, each
// with its lines per slot (FR-25).
func (s *Service) ListRestockSessions(ctx context.Context, q RestockQuery) (RestockPage, error) {
	r, err := s.parseRange(q.From, q.To, 7, 731)
	if err != nil {
		return RestockPage{}, err
	}
	page, size := pageOf(q.Page, q.PageSize, 20)

	where := ` WHERE rs.started_at >= ? AND rs.started_at < ?`
	args := []any{r.Start, r.End}
	if q.MachineID != 0 {
		where += ` AND rs.machine_id = ?`
		args = append(args, q.MachineID)
	}
	if q.UserID != 0 {
		where += ` AND rs.user_id = ?`
		args = append(args, q.UserID)
	}
	out := RestockPage{Page: page, PageSize: size, Rows: []RestockSessionView{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM restock_sessions rs`+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	limit, offset := size, (page-1)*size
	if q.All {
		limit, offset = maxExportRows, 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.id, rs.client_id, rs.started_at, rs.finished_at, rs.note,
		       m.id, m.code, m.name, u.id, u.name, u.username
		FROM restock_sessions rs
		JOIN machines m ON m.id = rs.machine_id
		JOIN users u ON u.id = rs.user_id`+where+`
		ORDER BY rs.started_at DESC, rs.id DESC
		LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return out, err
	}
	index := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var v RestockSessionView
		var started, finished sql.NullTime
		var note sql.NullString
		if err := rows.Scan(&v.ID, &v.ClientID, &started, &finished, &note, &v.Machine.ID, &v.Machine.Code, &v.Machine.Name,
			&v.Operator.ID, &v.Operator.Name, &v.Operator.Username); err != nil {
			rows.Close()
			return out, err
		}
		v.StartedAt, v.FinishedAt, v.Note = ms(started.Time), ms(finished.Time), nullString(note)
		v.Items = []RestockItemView{}
		index[v.ID] = len(out.Rows)
		ids = append(ids, v.ID)
		out.Rows = append(out.Rows, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}

	// The lines of every session on this page, in one query.
	for start := 0; start < len(ids); start += 1000 {
		chunk := ids[start:min(start+1000, len(ids))]
		irows, err := s.db.QueryContext(ctx, `
			SELECT ri.session_id, s.slot_number, ri.product_id, p.name, ri.qty_before, ri.qty_after, ri.product_changed
			FROM restock_items ri
			JOIN slots s ON s.id = ri.slot_id
			LEFT JOIN products p ON p.id = ri.product_id
			WHERE ri.session_id IN (`+placeholders(len(chunk))+`)
			ORDER BY ri.session_id, s.slot_number`, int64sToArgs(chunk)...)
		if err != nil {
			return out, err
		}
		for irows.Next() {
			var sid int64
			var it RestockItemView
			var pid sql.NullInt64
			var pname sql.NullString
			if err := irows.Scan(&sid, &it.SlotNumber, &pid, &pname, &it.QtyBefore, &it.QtyAfter, &it.ProductChanged); err != nil {
				irows.Close()
				return out, err
			}
			it.ProductID, it.ProductName = nullInt(pid), nullString(pname)
			v := &out.Rows[index[sid]]
			v.Items = append(v.Items, it)
			if it.ProductChanged {
				v.ItemsAdded += it.QtyAfter // the coil was emptied and refilled with the new product
			} else if d := it.QtyAfter - it.QtyBefore; d > 0 {
				v.ItemsAdded += d
			}
			if it.ProductChanged || it.QtyAfter != it.QtyBefore {
				v.SlotsChanged++
			}
		}
		irows.Close()
		if err := irows.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- kiosk: operator visit (UC-05)

// OperatorSlots is what the restock screen shows: every slot with its count,
// capacity and planned product change (FR-14).
func (s *Service) OperatorSlots(ctx context.Context, m Machine) (OperatorSlots, error) {
	slots, err := s.slotViews(ctx, s.db, m.ID)
	if err != nil {
		return OperatorSlots{}, err
	}
	return OperatorSlots{Machine: MachineInfo{Code: m.Code, Name: m.Name}, Slots: slots}, nil
}

// RestockInput is the body the kiosk sends when the operator confirms a visit.
type RestockInput struct {
	ID         string             `json:"id"`         // the kiosk's UUID for this visit; a resend with the same id is not saved twice
	FinishedAt json.RawMessage    `json:"finishedAt"` // optional: ISO 8601 (as the kiosk sends it) or Unix milliseconds
	Note       string             `json:"note"`
	Items      []RestockItemInput `json:"items"`
}

// RestockItemInput is one slot of a visit. qtyBefore is ignored: the server uses its own count.
type RestockItemInput struct {
	SlotNumber json.Number     `json:"slotNumber"`
	QtyAfter   json.RawMessage `json:"qtyAfter"` // read by hand so "", 2.5 or null are refused as INVALID_INPUT, not as unreadable JSON
}

type lockedSlot struct {
	id       int64
	number   int
	capacity int
	qty      int
	product  sql.NullInt64
	next     sql.NullInt64
}

// SaveRestock records one operator visit (FR-15): a session with a line per
// slot (product, count before, count after), applies planned product changes,
// clears blocked slots and raises the machine's catalog version so the kiosk
// reloads. Counts outside 0..capacity are refused and nothing is saved
// (TC-14 to TC-16).
func (s *Service) SaveRestock(ctx context.Context, m Machine, p Principal, in RestockInput) (RestockSaved, error) {
	clientID, ok := normalizeUUID(in.ID)
	if !ok {
		return RestockSaved{}, invalidInput("ID kunjungan harus UUID.", map[string]string{"id": "UUID tidak valid"})
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Note)) > 255 {
		return RestockSaved{}, invalidInput("Catatan paling panjang 255 karakter.", map[string]string{"note": "terlalu panjang"})
	}
	// Read and check every requested count before touching the database.
	want := map[int]int{}
	bad := map[string]string{}
	var badSlots []string
	for _, it := range in.Items {
		n, ok := intField(it.SlotNumber)
		key := "slot" + it.SlotNumber.String()
		if !ok || n < 1 || n > 60 {
			bad[key] = "nomor slot tidak valid"
			badSlots = append(badSlots, it.SlotNumber.String())
			continue
		}
		if _, dup := want[int(n)]; dup {
			bad[key] = "slot dikirim dua kali"
			badSlots = append(badSlots, it.SlotNumber.String())
			continue
		}
		qty, ok := rawInt(it.QtyAfter)
		if !ok {
			qty = -1
		}
		if qty < 0 || qty > 255 {
			bad[key] = "jumlah harus bilangan bulat 0 sampai kapasitas"
			badSlots = append(badSlots, it.SlotNumber.String())
			continue
		}
		want[int(n)] = int(qty)
	}
	if len(bad) > 0 {
		return RestockSaved{}, invalidInput("Jumlah slot "+strings.Join(badSlots, ", ")+" tidak valid.", bad)
	}

	var saved RestockSaved
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		// Lock order is always machine, then slots, as in a purchase, so the two never deadlock.
		// Taking the machine lock first also serializes two sends of the same visit.
		var version int64
		if err := tx.QueryRowContext(ctx, `SELECT catalog_version FROM machines WHERE id = ? FOR UPDATE`, m.ID).Scan(&version); err != nil {
			return err
		}

		// A resend of a visit already saved returns the first result (idempotent).
		var sid int64
		var mid int64
		var finished time.Time
		err := tx.QueryRowContext(ctx, `SELECT id, machine_id, finished_at FROM restock_sessions WHERE client_id = ?`, clientID).Scan(&sid, &mid, &finished)
		if err == nil {
			if mid != m.ID {
				return errConflict("CONFLICT", "ID kunjungan sudah dipakai mesin lain.", nil)
			}
			saved = RestockSaved{ID: clientID, SessionID: sid, SavedAt: ms(finished), CatalogVersion: version}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		rows, err := tx.QueryContext(ctx, `SELECT id, slot_number, capacity, qty, product_id, next_product_id
			FROM slots WHERE machine_id = ? ORDER BY slot_number FOR UPDATE`, m.ID)
		if err != nil {
			return err
		}
		var slots []lockedSlot
		for rows.Next() {
			var sl lockedSlot
			if err := rows.Scan(&sl.id, &sl.number, &sl.capacity, &sl.qty, &sl.product, &sl.next); err != nil {
				rows.Close()
				return err
			}
			slots = append(slots, sl)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		bad := map[string]string{}
		var badSlots []string
		known := map[int]bool{}
		for _, sl := range slots {
			known[sl.number] = true
			q, sent := want[sl.number]
			if !sent {
				continue
			}
			key := "slot" + strconv.Itoa(sl.number)
			after := sl.product
			if sl.next.Valid {
				after = sl.next
			}
			switch {
			case q > sl.capacity:
				bad[key] = "maksimal " + strconv.Itoa(sl.capacity)
				badSlots = append(badSlots, strconv.Itoa(sl.number))
			case q > 0 && !after.Valid:
				bad[key] = "slot belum punya produk, isi harus 0"
				badSlots = append(badSlots, strconv.Itoa(sl.number))
			}
		}
		for n := range want {
			if !known[n] {
				bad["slot"+strconv.Itoa(n)] = "slot tidak ada di mesin ini"
				badSlots = append(badSlots, strconv.Itoa(n))
			}
		}
		if len(bad) > 0 {
			return invalidInput("Jumlah slot "+strings.Join(badSlots, ", ")+" tidak valid.", bad)
		}

		now := s.now()
		finishedAt := now
		if t, ok := rawTime(in.FinishedAt); ok && !t.Before(p.StartedAt) && !t.After(now) {
			// The agent may resend a visit it journaled earlier; keep its time when it is plausible.
			finishedAt = t
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO restock_sessions (client_id, machine_id, user_id, started_at, finished_at, note)
			VALUES (?, ?, ?, ?, ?, ?)`, clientID, m.ID, p.UserID, p.StartedAt, finishedAt, strOrNil(strings.TrimSpace(in.Note)))
		if err != nil {
			if isMySQLError(err, mysqlDuplicateKey) {
				return errConflict("CONFLICT", "Kunjungan ini sedang disimpan. Coba lagi.", nil)
			}
			return err
		}
		if sid, err = res.LastInsertId(); err != nil {
			return err
		}

		var values strings.Builder
		args := make([]any, 0, len(slots)*6)
		for i, sl := range slots {
			after := sl.product
			changed := sl.next.Valid
			if changed {
				after = sl.next
			}
			q, sent := want[sl.number]
			if !sent {
				q = sl.qty
				if changed {
					q = 0 // a product swap with no count sent: the new product's count is unknown, so start at 0
				}
			}
			if i > 0 {
				values.WriteByte(',')
			}
			values.WriteString("(?, ?, ?, ?, ?, ?)")
			args = append(args, sid, sl.id, after, sl.qty, q, changed)
			if _, err := tx.ExecContext(ctx, `UPDATE slots SET product_id = ?, next_product_id = NULL, qty = ?, needs_check = FALSE WHERE id = ?`,
				after, q, sl.id); err != nil {
				return err
			}
		}
		if len(slots) > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO restock_items (session_id, slot_id, product_id, qty_before, qty_after, product_changed) VALUES `+
				values.String(), args...); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE machines SET catalog_version = catalog_version + 1 WHERE id = ?`, m.ID); err != nil {
			return err
		}
		saved = RestockSaved{ID: clientID, SessionID: sid, SavedAt: ms(finishedAt), CatalogVersion: version + 1}
		return nil
	})
	return saved, err
}

// rawTime reads a time sent either as an ISO 8601 string or as Unix milliseconds.
func rawTime(raw json.RawMessage) (time.Time, bool) {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		t, err := time.Parse(time.RFC3339Nano, str)
		return t.UTC().Truncate(time.Millisecond), err == nil
	}
	if v, ok := rawInt(raw); ok {
		return time.UnixMilli(v).UTC(), true
	}
	return time.Time{}, false
}
