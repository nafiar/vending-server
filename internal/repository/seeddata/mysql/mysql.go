// Package mysql is the demo-data driver.
//
// Everything here is a bulk write. The inserts are batched because the dataset
// is tens of thousands of rows and one statement per row would make filling a
// demo server a coffee break; the batch sizes are a compromise between the
// round trips saved and the size of one statement.
package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/seeddata"
)

// How many rows go in one INSERT. Transactions are the bulk of the data, so
// they get the bigger batch; a visit carries its lines with it, which makes
// each one several statements' worth of arguments.
const (
	txBatch    = 500
	visitBatch = 200
)

// MySQL writes the demo dataset.
//
// It holds the pool rather than a Queryer: Write needs its own transaction and
// Wipe needs a single connection, and neither can be handed one from outside.
type MySQL struct{ db *sql.DB }

// Compile-time proof that the driver still matches the port.
var _ seeddata.Repository = (*MySQL)(nil)

// New returns the driver over the pool.
func New(db *sql.DB) *MySQL { return &MySQL{db: db} }

func (s *MySQL) MachineKeyHashes(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code, api_key_hash FROM machines`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var code, hash string
		if err := rows.Scan(&code, &hash); err != nil {
			return nil, err
		}
		out[code] = hash
	}
	return out, rows.Err()
}

func (s *MySQL) CountOccupied(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users)
		+ (SELECT COUNT(*) FROM machines) + (SELECT COUNT(*) FROM products)`).Scan(&n)
	return n, err
}

// Wipe empties every table, child tables first.
//
// It runs on one connection taken out of the pool, because the foreign-key
// switch is a session setting: on another connection the TRUNCATEs would still
// be refused by the constraints. The switch is put back on the way out, on that
// same connection, before it is returned to the pool.
func (s *MySQL) Wipe(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET FOREIGN_KEY_CHECKS = 0`); err != nil {
		return err
	}
	// Not ctx: this has to run even when the load was cancelled, or the
	// connection goes back to the pool with the constraints switched off.
	defer conn.ExecContext(context.Background(), `SET FOREIGN_KEY_CHECKS = 1`) //nolint:errcheck

	for _, t := range []string{
		"sessions", "audit_logs", "restock_items", "restock_sessions",
		"transactions", "slots", "machines", "products", "users",
	} {
		if _, err := conn.ExecContext(ctx, "TRUNCATE TABLE "+t); err != nil {
			return fmt.Errorf("empty %s: %w", t, err)
		}
	}
	return nil
}

// Write loads the dataset in one transaction, parents before children.
func (s *MySQL) Write(ctx context.Context, d model.DemoData) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	for _, u := range d.Users {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users
			(id, name, username, password_hash, role, is_active, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			u.ID, u.Name, u.Username, u.PasswordHash, u.Role, u.IsActive, u.CreatedAt); err != nil {
			return fmt.Errorf("insert users: %w", err)
		}
	}
	for _, p := range d.Products {
		if _, err := tx.ExecContext(ctx, `INSERT INTO products
			(id, name, description, price, image_url, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.Name, p.Description, p.Price, p.ImageURL, p.IsActive, p.CreatedAt, p.UpdatedAt); err != nil {
			return fmt.Errorf("insert products: %w", err)
		}
	}
	for _, m := range d.Machines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO machines
			(id, code, name, location, api_key_hash, catalog_version, is_active, last_seen_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, TRUE, ?, ?)`,
			m.ID, m.Code, m.Name, m.Location, m.KeyHash, m.CatalogVersion, m.LastSeenAt, m.CreatedAt); err != nil {
			return fmt.Errorf("insert machines: %w", err)
		}
	}
	for _, sl := range d.Slots {
		if _, err := tx.ExecContext(ctx, `INSERT INTO slots
			(id, machine_id, slot_number, product_id, next_product_id, capacity, qty, needs_check)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sl.ID, sl.MachineID, sl.Number, sl.ProductID, sl.NextProductID,
			sl.Capacity, sl.Qty, sl.NeedsCheck); err != nil {
			return fmt.Errorf("insert slots: %w", err)
		}
	}
	if err := insertVisits(ctx, tx, d.Visits); err != nil {
		return err
	}
	if err := insertTransactions(ctx, tx, d.Transactions); err != nil {
		return err
	}
	for _, a := range d.Audits {
		before, err := encode(a.Before)
		if err != nil {
			return err
		}
		after, err := encode(a.After)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs
			(user_id, action, entity_type, entity_id, entity_label, before_json, after_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ActorID, a.Action, a.EntityType, a.EntityID, a.Label, before, after, a.At); err != nil {
			return fmt.Errorf("insert audit logs: %w", err)
		}
	}
	return tx.Commit()
}

func insertTransactions(ctx context.Context, tx *sql.Tx, rows []model.DemoTransaction) error {
	const columns = 11
	for start := 0; start < len(rows); start += txBatch {
		chunk := rows[start:min(start+txBatch, len(rows))]
		args := make([]any, 0, len(chunk)*columns)
		for _, r := range chunk {
			args = append(args, r.ID, r.MachineID, r.SlotID, r.ProductID, r.Price, r.Status,
				r.FailureReason, r.CustomerReportedAt, r.CreatedAt, r.PaidAt, r.FinishedAt)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO transactions
			(id, machine_id, slot_id, product_id, price, status, failure_reason,
			 customer_reported_at, created_at, paid_at, finished_at)
			VALUES `+mysqldb.Rows(len(chunk), columns), args...); err != nil {
			return fmt.Errorf("insert transactions: %w", err)
		}
	}
	return nil
}

// insertVisits writes each batch of visits and then the lines of exactly those
// visits, so the two statements cannot disagree about which visits exist.
func insertVisits(ctx context.Context, tx *sql.Tx, visits []model.DemoVisit) error {
	const visitColumns, itemColumns = 6, 6
	for start := 0; start < len(visits); start += visitBatch {
		chunk := visits[start:min(start+visitBatch, len(visits))]
		args := make([]any, 0, len(chunk)*visitColumns)
		var items []any
		lines := 0
		for _, v := range chunk {
			args = append(args, v.ID, v.ClientID, v.MachineID, v.UserID, v.StartedAt, v.FinishedAt)
			for _, it := range v.Items {
				items = append(items, v.ID, it.SlotID, it.ProductID, it.QtyBefore, it.QtyAfter, it.ProductChanged)
				lines++
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO restock_sessions
			(id, client_id, machine_id, user_id, started_at, finished_at)
			VALUES `+mysqldb.Rows(len(chunk), visitColumns), args...); err != nil {
			return fmt.Errorf("insert restock sessions: %w", err)
		}
		if lines == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO restock_items
			(session_id, slot_id, product_id, qty_before, qty_after, product_changed)
			VALUES `+mysqldb.Rows(lines, itemColumns), items...); err != nil {
			return fmt.Errorf("insert restock items: %w", err)
		}
	}
	return nil
}

// encode stores a change-log side as JSON, or NULL when there was none.
func encode(m map[string]any) (any, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}
