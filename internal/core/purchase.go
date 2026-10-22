package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// The purchase state machine (PRD, "Purchase sequence and transaction states"):
//
//	POST /transactions            -> PENDING   (catalog version and availability checked, price copied)
//	POST /transactions/{id}/pay   PENDING -> PAID      (refused after the 60 s window: EXPIRED)
//	POST /transactions/{id}/result PAID -> DISPENSED (count - 1) or FAILED (slot blocked)
//	POST /transactions/{id}/cancel PENDING -> CANCELLED or EXPIRED
//
// Final states never change (BR-11). Every call is safe to repeat: the kiosk
// generates the transaction id, and a repeated call returns the current state
// without changing anything (NFR-03, TC-08).

const reportWindow = 5 * time.Minute // the kiosk offers the button for 60 s; the rest covers retries

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	reasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,49}$`)
)

func normalizeUUID(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, uuidPattern.MatchString(s)
}

func errSoldOut() *Error {
	return errConflict("SOLD_OUT", "Produk habis.", nil)
}

func errTxNotFound() *Error { return errNotFound("Transaksi tidak ditemukan.") }

// ---------------------------------------------------------------- catalog (UC-01)

// GetCatalog is what the kiosk shows (FR-01): every slot with its product and
// whether it can be bought now. available follows BR-04: the machine is active,
// the slot has an active product, it is not blocked, and its count minus open
// transactions (PENDING within the payment window, or PAID) is above zero.
func (s *Service) GetCatalog(ctx context.Context, m Machine) (Catalog, error) {
	// One read-only transaction gives the version and the slots from the same snapshot.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Catalog{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	c, err := s.catalog(ctx, tx, m.ID)
	if err != nil {
		return Catalog{}, err
	}
	return c, tx.Commit()
}

func (s *Service) catalog(ctx context.Context, q queryer, machineID int64) (Catalog, error) {
	var c Catalog
	var active bool
	if err := q.QueryRowContext(ctx, `SELECT catalog_version, code, name, is_active FROM machines WHERE id = ?`, machineID).
		Scan(&c.CatalogVersion, &c.Machine.Code, &c.Machine.Name, &active); err != nil {
		return c, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT s.slot_number, s.qty, s.needs_check, p.id, p.name, p.description, p.price, p.image_url, p.is_active,
		       (SELECT COUNT(*) FROM transactions t
		        WHERE t.slot_id = s.id AND (t.status = 'PAID' OR (t.status = 'PENDING' AND t.created_at > ?))) AS open
		FROM slots s LEFT JOIN products p ON p.id = s.product_id
		WHERE s.machine_id = ?
		ORDER BY s.slot_number`, s.now().Add(-s.paymentWindow), machineID)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	c.Slots = []CatalogSlot{}
	for rows.Next() {
		var sl CatalogSlot
		var qty, open int
		var blocked bool
		var pid, price sql.NullInt64
		var name, desc, img sql.NullString
		var pActive sql.NullBool
		if err := rows.Scan(&sl.SlotNumber, &qty, &blocked, &pid, &name, &desc, &price, &img, &pActive, &open); err != nil {
			return c, err
		}
		if pid.Valid {
			sl.Product = &ProductBrief{ID: pid.Int64, Name: name.String, Description: desc.String, Price: price.Int64, ImageURL: nullString(img)}
		}
		sl.Available = active && pid.Valid && pActive.Bool && !blocked && qty-open > 0
		c.Slots = append(c.Slots, sl)
	}
	return c, rows.Err()
}

// ---------------------------------------------------------------- create (UC-02, FR-04)

// CreateTransactionInput is the body of POST /transactions.
type CreateTransactionInput struct {
	ID             string      `json:"id"`
	SlotNumber     json.Number `json:"slotNumber"`
	CatalogVersion json.Number `json:"catalogVersion"`
}

type txRow struct {
	id         string
	machineID  int64
	slotID     int64
	slotNumber int
	status     string
	price      int64
	reason     sql.NullString
	created    time.Time
	paid       sql.NullTime
	finished   sql.NullTime
	reported   sql.NullTime
	product    ProductBrief
}

func (s *Service) loadTx(ctx context.Context, q queryer, id string, lock bool) (txRow, error) {
	query := `SELECT t.id, t.machine_id, t.slot_id, s.slot_number, t.status, t.price, t.failure_reason,
		t.created_at, t.paid_at, t.finished_at, t.customer_reported_at, p.id, p.name, p.description, p.image_url
		FROM transactions t JOIN slots s ON s.id = t.slot_id JOIN products p ON p.id = t.product_id
		WHERE t.id = ?`
	if lock {
		// Only the transaction row is locked (MySQL locks every joined row with FOR UPDATE,
		// so the lock is taken on the plain row first).
		var dummy string
		if err := q.QueryRowContext(ctx, `SELECT id FROM transactions WHERE id = ? FOR UPDATE`, id).Scan(&dummy); err != nil {
			return txRow{}, err
		}
	}
	var r txRow
	var desc, img sql.NullString
	err := q.QueryRowContext(ctx, query, id).Scan(&r.id, &r.machineID, &r.slotID, &r.slotNumber, &r.status, &r.price, &r.reason,
		&r.created, &r.paid, &r.finished, &r.reported, &r.product.ID, &r.product.Name, &desc, &img)
	r.product.Description, r.product.ImageURL, r.product.Price = desc.String, nullString(img), r.price
	return r, err
}

func (r txRow) state() TransactionState {
	return TransactionState{
		ID: r.id, Status: r.status, FailureReason: nullString(r.reason),
		PaidAt: nullMs(r.paid), FinishedAt: nullMs(r.finished), CustomerReportedAt: nullMs(r.reported),
	}
}

func (s *Service) created(r txRow) CreatedTransaction {
	left := int((s.paymentWindow - s.now().Sub(r.created)) / time.Second)
	if left < 0 || r.status != "PENDING" {
		left = 0
	}
	return CreatedTransaction{
		ID: r.id, SlotNumber: r.slotNumber, Product: r.product, Amount: r.price,
		ExpiresInSeconds: left, Status: r.status, CreatedAt: ms(r.created),
	}
}

// CreateTransaction starts a purchase. The kiosk sends the catalog version its
// screen was built from; an old one is refused with the fresh catalog
// (CATALOG_STALE, TC-10), so a stale screen can never sell the wrong product or
// price. The price is copied into the transaction (BR-05, TC-09).
func (s *Service) CreateTransaction(ctx context.Context, m Machine, in CreateTransactionInput) (CreatedTransaction, error) {
	id, ok := normalizeUUID(in.ID)
	if !ok {
		return CreatedTransaction{}, invalidInput("ID transaksi harus UUID.", map[string]string{"id": "UUID tidak valid"})
	}
	slotNumber, ok1 := intField(in.SlotNumber)
	version, ok2 := intField(in.CatalogVersion)
	if !ok1 || !ok2 {
		return CreatedTransaction{}, invalidInput("slotNumber dan catalogVersion wajib berupa angka.", nil)
	}

	var out CreatedTransaction
	var stale bool
	attempt := func() error {
		return s.withTx(ctx, func(tx *sql.Tx) error {
			stale = false
			// A retried request returns the transaction it already created.
			if r, err := s.loadTx(ctx, tx, id, false); err == nil {
				if r.machineID != m.ID {
					return errTxNotFound()
				}
				if r.slotNumber != int(slotNumber) {
					return errConflict("CONFLICT", "ID transaksi sudah dipakai untuk slot lain.", nil)
				}
				out = s.created(r)
				return nil
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}

			// Lock order everywhere: machine, then slot.
			var current int64
			var active bool
			if err := tx.QueryRowContext(ctx, `SELECT catalog_version, is_active FROM machines WHERE id = ? LOCK IN SHARE MODE`, m.ID).
				Scan(&current, &active); err != nil {
				return err
			}
			if !active {
				return &Error{Status: http.StatusForbidden, Code: "MACHINE_INACTIVE", Message: "Mesin ini dinonaktifkan di panel admin."}
			}
			if current != version {
				stale = true
				return nil
			}

			var slotID int64
			var qty int
			var blocked bool
			var productID sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT id, qty, needs_check, product_id FROM slots WHERE machine_id = ? AND slot_number = ? FOR UPDATE`,
				m.ID, slotNumber).Scan(&slotID, &qty, &blocked, &productID)
			if errors.Is(err, sql.ErrNoRows) {
				return errNotFound("Slot tidak ditemukan.")
			}
			if err != nil {
				return err
			}
			// Expire this slot's abandoned payments first, so they stop holding items (BR-06).
			now := s.now()
			if _, err := tx.ExecContext(ctx, `UPDATE transactions SET status = 'EXPIRED', finished_at = DATE_ADD(created_at, INTERVAL ? SECOND)
				WHERE slot_id = ? AND status = 'PENDING' AND created_at < ?`,
				int(s.paymentWindow/time.Second), slotID, now.Add(-s.paymentWindow)); err != nil {
				return err
			}
			// A locking read: it sees every purchase committed before this slot lock was granted,
			// not the snapshot this transaction started with, so two requests can't both take the last item.
			var open int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM transactions WHERE slot_id = ? AND status IN ('PENDING', 'PAID') LOCK IN SHARE MODE`,
				slotID).Scan(&open); err != nil {
				return err
			}
			if !productID.Valid || blocked || qty-open <= 0 {
				return errSoldOut()
			}
			var p ProductBrief
			var desc, img sql.NullString
			var pActive bool
			if err := tx.QueryRowContext(ctx, `SELECT id, name, description, price, image_url, is_active FROM products WHERE id = ?`, productID.Int64).
				Scan(&p.ID, &p.Name, &desc, &p.Price, &img, &pActive); err != nil {
				return err
			}
			if !pActive {
				return errSoldOut()
			}
			p.Description, p.ImageURL = desc.String, nullString(img)
			if _, err := tx.ExecContext(ctx, `INSERT INTO transactions (id, machine_id, slot_id, product_id, price, status, created_at)
				VALUES (?, ?, ?, ?, ?, 'PENDING', ?)`, id, m.ID, slotID, p.ID, p.Price, now); err != nil {
				return err
			}
			out = CreatedTransaction{ID: id, SlotNumber: int(slotNumber), Product: p, Amount: p.Price,
				ExpiresInSeconds: int(s.paymentWindow / time.Second), Status: "PENDING", CreatedAt: ms(now)}
			return nil
		})
	}
	err := attempt()
	if isMySQLError(err, mysqlDuplicateKey) {
		err = attempt() // the same id was created concurrently; the retry finds it
	}
	if err != nil {
		return CreatedTransaction{}, err
	}
	if stale {
		fresh, err := s.GetCatalog(ctx, m)
		if err != nil {
			return CreatedTransaction{}, err
		}
		return CreatedTransaction{}, errConflict("CATALOG_STALE", "Info produk diperbarui.", map[string]any{"catalog": fresh})
	}
	return out, nil
}

// ---------------------------------------------------------------- pay (UC-03)

// Pay confirms the simulated QRIS payment: PENDING -> PAID. After the 60 s
// window it is refused and the transaction becomes EXPIRED (BR-06, TC-05).
// Paying again returns the current state, so the agent can resume after a restart.
func (s *Service) Pay(ctx context.Context, m Machine, id string) (TransactionState, error) {
	id, _ = normalizeUUID(id)
	var out TransactionState
	var expired bool
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		expired = false
		r, err := s.loadTx(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && r.machineID != m.ID) {
			return errTxNotFound()
		}
		if err != nil {
			return err
		}
		now := s.now()
		switch r.status {
		case "PENDING":
			if now.Sub(r.created) > s.paymentWindow {
				_, err := tx.ExecContext(ctx, `UPDATE transactions SET status = 'EXPIRED', finished_at = ? WHERE id = ?`, r.created.Add(s.paymentWindow), id)
				expired = true
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE transactions SET status = 'PAID', paid_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
			r.status, r.paid = "PAID", sql.NullTime{Time: now, Valid: true}
		case "EXPIRED":
			expired = true
			return nil
		case "CANCELLED":
			return errConflict("TRANSACTION_CLOSED", "Transaksi sudah dibatalkan.", nil)
		}
		out = r.state()
		return nil
	})
	if err != nil {
		return TransactionState{}, err
	}
	if expired {
		return TransactionState{}, newErr(http.StatusGone, "EXPIRED", "Waktu pembayaran habis.")
	}
	return out, nil
}

// ---------------------------------------------------------------- result (FR-07, FR-08)

// ResultInput is the body of POST /transactions/{id}/result, sent by the agent
// after it turned the coil.
type ResultInput struct {
	Status        string `json:"status"`        // DISPENSED or FAILED
	FailureReason string `json:"failureReason"` // for FAILED: NO_ACK, MOTOR_TIMEOUT, PORT_ERROR or AGENT_RESTART
}

// RecordResult ends a paid transaction. DISPENSED subtracts 1 from the slot's
// count in the same database transaction (FR-07); FAILED leaves the count and
// blocks the slot until the next visit (BR-07, TC-06). A repeated report of the
// same result changes nothing (TC-08).
func (s *Service) RecordResult(ctx context.Context, m Machine, id string, in ResultInput) (TransactionState, error) {
	id, _ = normalizeUUID(id)
	status := strings.ToUpper(strings.TrimSpace(in.Status))
	reason := strings.ToUpper(strings.TrimSpace(in.FailureReason))
	switch status {
	case "DISPENSED":
		reason = ""
	case "FAILED":
		if !reasonPattern.MatchString(reason) {
			return TransactionState{}, invalidInput("failureReason wajib untuk FAILED, misalnya MOTOR_TIMEOUT.", map[string]string{"failureReason": "tidak valid"})
		}
	default:
		return TransactionState{}, invalidInput("status harus DISPENSED atau FAILED.", map[string]string{"status": "tidak valid"})
	}

	var out TransactionState
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := s.loadTx(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && r.machineID != m.ID) {
			return errTxNotFound()
		}
		if err != nil {
			return err
		}
		switch r.status {
		case "PAID":
		case "DISPENSED", "FAILED":
			if r.status != status {
				return errConflict("CONFLICT", "Hasil transaksi ini sudah tercatat sebagai "+r.status+".", map[string]any{"current": r.state()})
			}
			out = r.state() // a repeat: nothing changes
			return nil
		case "PENDING":
			return errConflict("NOT_PAID", "Transaksi belum dibayar.", nil)
		default:
			return errConflict("TRANSACTION_CLOSED", "Transaksi sudah "+strings.ToLower(r.status)+".", nil)
		}

		now := s.now()
		if status == "DISPENSED" {
			if _, err := tx.ExecContext(ctx, `UPDATE transactions SET status = 'DISPENSED', finished_at = ? WHERE id = ?`, now, id); err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, `UPDATE slots SET qty = qty - 1 WHERE id = ? AND qty > 0`, r.slotID)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				s.log.Warn("dispensed from a slot whose count is already 0; check it at the next visit", "transaction", id, "slot_id", r.slotID)
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE transactions SET status = 'FAILED', failure_reason = ?, finished_at = ? WHERE id = ?`, reason, now, id); err != nil {
				return err
			}
			// Machine before slot, the same lock order as a purchase.
			if _, err := tx.ExecContext(ctx, `UPDATE machines SET catalog_version = catalog_version + 1 WHERE id = ?`, m.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE slots SET needs_check = TRUE WHERE id = ?`, r.slotID); err != nil {
				return err
			}
			r.reason = sql.NullString{String: reason, Valid: true}
		}
		r.status, r.finished = status, sql.NullTime{Time: now, Valid: true}
		out = r.state()
		return nil
	})
	return out, err
}

// ---------------------------------------------------------------- cancel, report, status

// Cancel ends an unpaid transaction: "Batal" gives CANCELLED and the kiosk's
// 60 s timer gives EXPIRED (FR-11). Anything else returns the current state.
func (s *Service) Cancel(ctx context.Context, m Machine, id, reason string) (TransactionState, error) {
	id, _ = normalizeUUID(id)
	next := "CANCELLED"
	if strings.EqualFold(reason, "TIMEOUT") {
		next = "EXPIRED"
	}
	var out TransactionState
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := s.loadTx(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && r.machineID != m.ID) {
			return errTxNotFound()
		}
		if err != nil {
			return err
		}
		if r.status == "PENDING" {
			now := s.now()
			if _, err := tx.ExecContext(ctx, `UPDATE transactions SET status = ?, finished_at = ? WHERE id = ?`, next, now, id); err != nil {
				return err
			}
			r.status, r.finished = next, sql.NullTime{Time: now, Valid: true}
		}
		out = r.state()
		return nil
	})
	return out, err
}

// Report records "Produk tidak keluar?" on a DISPENSED sale (FR-09, TC-12).
// The sale keeps its status and still counts as revenue, flagged for review (BR-03).
func (s *Service) Report(ctx context.Context, m Machine, id string) (ReportResult, error) {
	id, _ = normalizeUUID(id)
	var out ReportResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		r, err := s.loadTx(ctx, tx, id, true)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && r.machineID != m.ID) {
			return errTxNotFound()
		}
		if err != nil {
			return err
		}
		if r.status != "DISPENSED" {
			return errConflict("NOT_REPORTABLE", "Hanya penjualan yang berhasil yang bisa dilaporkan.", nil)
		}
		if r.reported.Valid {
			out = ReportResult{ID: id, Reported: true, ReportedAt: ms(r.reported.Time)}
			return nil
		}
		now := s.now()
		if r.finished.Valid && now.Sub(r.finished.Time) > reportWindow {
			return errConflict("REPORT_WINDOW_CLOSED", "Laporan hanya bisa dikirim sesaat setelah pembelian.", nil)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE transactions SET customer_reported_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
		out = ReportResult{ID: id, Reported: true, ReportedAt: ms(now)}
		return nil
	})
	return out, err
}

// TransactionStatus returns one transaction's state, for an agent that
// restarted mid-purchase and needs to know where it stopped (BR-14).
func (s *Service) TransactionStatus(ctx context.Context, m Machine, id string) (TransactionState, error) {
	id, _ = normalizeUUID(id)
	r, err := s.loadTx(ctx, s.db, id, false)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && r.machineID != m.ID) {
		return TransactionState{}, errTxNotFound()
	}
	if err != nil {
		return TransactionState{}, err
	}
	return r.state(), nil
}

// Heartbeat records the agent's contact time (FR-28) and returns the current
// catalog version, so the agent can reload early when an admin changed something.
func (s *Service) Heartbeat(ctx context.Context, m Machine) (HeartbeatResult, error) {
	now := s.now()
	if _, err := s.db.ExecContext(ctx, `UPDATE machines SET last_seen_at = ? WHERE id = ?`, now, m.ID); err != nil {
		return HeartbeatResult{}, err
	}
	var v int64
	if err := s.db.QueryRowContext(ctx, `SELECT catalog_version FROM machines WHERE id = ?`, m.ID).Scan(&v); err != nil {
		return HeartbeatResult{}, err
	}
	return HeartbeatResult{OK: true, ServerTime: ms(now), CatalogVersion: v}, nil
}
