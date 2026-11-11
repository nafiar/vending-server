// Package mysql is the restock driver.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/restock"
)

// MySQL reads and writes the restock_sessions and restock_items tables.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ restock.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool or one transaction.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

func (s *MySQL) ByClientID(ctx context.Context, clientID string) (model.RestockSession, error) {
	var r model.RestockSession
	var note sql.NullString
	var started, finished sql.NullTime
	err := s.q.QueryRowContext(ctx, `
		SELECT id, client_id, machine_id, user_id, started_at, finished_at, note
		FROM restock_sessions WHERE client_id = ?`, clientID).
		Scan(&r.ID, &r.ClientID, &r.MachineID, &r.UserID, &started, &finished, &note)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RestockSession{}, model.ErrNoRecord
	}
	r.StartedAt, r.FinishedAt, r.Note = started.Time, finished.Time, note.String
	return r, err
}

func (s *MySQL) Create(ctx context.Context, r model.RestockSession) (int64, error) {
	res, err := s.q.ExecContext(ctx, `INSERT INTO restock_sessions
		(client_id, machine_id, user_id, started_at, finished_at, note)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ClientID, r.MachineID, r.UserID, r.StartedAt, r.FinishedAt,
		mysqldb.StrOrNil(strings.TrimSpace(r.Note)))
	if err != nil {
		if mysqldb.IsDuplicateKey(err) {
			return 0, model.ErrDuplicateID
		}
		return 0, err
	}
	return res.LastInsertId()
}

func (s *MySQL) AddItems(ctx context.Context, sessionID int64, items []model.RestockItem) error {
	if len(items) == 0 {
		return nil
	}
	var values strings.Builder
	args := make([]any, 0, len(items)*6)
	for i, it := range items {
		if i > 0 {
			values.WriteByte(',')
		}
		values.WriteString("(?, ?, ?, ?, ?, ?)")
		args = append(args, sessionID, it.SlotID, it.ProductID, it.QtyBefore, it.QtyAfter, it.ProductChanged)
	}
	_, err := s.q.ExecContext(ctx, `INSERT INTO restock_items
		(session_id, slot_id, product_id, qty_before, qty_after, product_changed) VALUES `+
		values.String(), args...)
	return err
}
