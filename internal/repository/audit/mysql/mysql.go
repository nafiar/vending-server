// Package mysql is the audit log driver.
package mysql

import (
	"context"
	"database/sql"
	"encoding/json"

	"vending/internal/model"
	"vending/internal/repository/audit"
	"vending/internal/repository/mysqldb"
)

// MySQL writes and reads the audit_logs table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches both ports. One struct
// serves them both: bound to a transaction it is the writer uow.Tx hands out,
// and bound to the pool it is the reader the change-log screen uses.
var (
	_ audit.Repository = (*MySQL)(nil)
	_ audit.Reader     = (*MySQL)(nil)
)

// New returns the driver over q. For a Write this is always one transaction,
// never the pool: see the package comment on repository/audit.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

func (s *MySQL) Write(ctx context.Context, e model.AuditEntry) error {
	before, err := encode(e.Before)
	if err != nil {
		return err
	}
	after, err := encode(e.After)
	if err != nil {
		return err
	}
	_, err = s.q.ExecContext(ctx, `INSERT INTO audit_logs
		(user_id, action, entity_type, entity_id, entity_label, before_json, after_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ActorID, e.Action, e.EntityType, e.EntityID,
		truncate(e.Label, model.MaxAuditLabelRunes), before, after, e.At)
	return err
}

// List reads the change log back, newest first. The actor's name is joined in
// here because the alternative is one lookup per row.
//
// before_json and after_json are handed back as the bytes they were stored as.
// They are copied out of the driver's buffer, which the next Scan reuses.
func (s *MySQL) List(ctx context.Context, f model.AuditFilter) ([]model.AuditLog, error) {
	where := "a.created_at >= ? AND a.created_at < ?"
	args := []any{f.Start, f.End}
	if f.EntityType != "" {
		where += " AND a.entity_type = ?"
		args = append(args, f.EntityType)
	}
	rows, err := s.q.QueryContext(ctx, `
		SELECT a.id, a.created_at, a.user_id, u.name, a.action, a.entity_type,
		       a.entity_id, a.entity_label, a.before_json, a.after_json
		FROM audit_logs a
		JOIN users u ON u.id = a.user_id
		WHERE `+where+`
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT ?`, append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.AuditLog{}
	for rows.Next() {
		var e model.AuditLog
		var at sql.NullTime
		var before, after []byte
		if err := rows.Scan(&e.ID, &at, &e.ActorID, &e.ActorName, &e.Action, &e.EntityType,
			&e.EntityID, &e.EntityLabel, &before, &after); err != nil {
			return nil, err
		}
		e.At = at.Time
		e.Before, e.After = clone(before), clone(after)
		out = append(out, e)
	}
	return out, rows.Err()
}

// clone copies the scanned bytes, which the driver reuses on the next row, and
// keeps nil as nil: a CREATE has no before, and that must stay distinguishable
// from an empty one.
func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// encode stores the changed fields as JSON, or NULL when there were none, which
// is what a CREATE has for its before.
func encode(m map[string]any) (any, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	return string(b), err
}

// truncate cuts to n characters, not n bytes: the label is a name an admin
// typed, and cutting a multi-byte character in half would store broken text.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
