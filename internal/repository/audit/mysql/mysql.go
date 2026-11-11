// Package mysql is the audit log driver.
package mysql

import (
	"context"
	"encoding/json"

	"vending/internal/model"
	"vending/internal/repository/audit"
	"vending/internal/repository/mysqldb"
)

// MySQL writes and reads the audit_logs table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ audit.Repository = (*MySQL)(nil)

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
