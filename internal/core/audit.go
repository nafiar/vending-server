package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
)

// Audit actions and entity types, as in the audit_logs ENUMs.
const (
	actionCreate     = "CREATE"
	actionUpdate     = "UPDATE"
	actionArchive    = "ARCHIVE"
	actionDeactivate = "DEACTIVATE"

	entityProduct = "PRODUCT"
	entityMachine = "MACHINE"
	entitySlot    = "SLOT"
	entityUser    = "USER"
)

type auditEntry struct {
	actorID    int64
	action     string
	entityType string
	entityID   int64
	label      string
	before     map[string]any // nil for CREATE
	after      map[string]any
}

// writeAudit records one change-log row (FR-26). It runs inside the caller's
// transaction, so a change and its log row are committed together or not at all.
func (s *Service) writeAudit(ctx context.Context, tx *sql.Tx, e auditEntry) error {
	enc := func(m map[string]any) (any, error) {
		if m == nil {
			return nil, nil
		}
		b, err := json.Marshal(m)
		return string(b), err
	}
	before, err := enc(e.before)
	if err != nil {
		return err
	}
	after, err := enc(e.after)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_logs (user_id, action, entity_type, entity_id, entity_label, before_json, after_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.actorID, e.action, e.entityType, e.entityID, truncate(e.label, 150), before, after, s.now())
	return err
}

// changedFields keeps only the keys whose value differs, so a log row shows
// exactly what changed: {price: 25000} -> {price: 30000}.
func changedFields(before, after map[string]any) (map[string]any, map[string]any, bool) {
	b, a := map[string]any{}, map[string]any{}
	for k, av := range after {
		if bv := before[k]; !reflect.DeepEqual(bv, av) {
			b[k] = bv
			a[k] = av
		}
	}
	return b, a, len(a) > 0
}

// AuditQuery filters the change log.
type AuditQuery struct {
	From, To   string
	EntityType string // PRODUCT, MACHINE, SLOT or USER; empty for all
}

// ListAuditLogs returns the change log, newest first (UC-11).
func (s *Service) ListAuditLogs(ctx context.Context, q AuditQuery) ([]AuditView, error) {
	r, err := s.parseRange(q.From, q.To, 120, 731)
	if err != nil {
		return nil, err
	}
	where := "a.created_at >= ? AND a.created_at < ?"
	args := []any{r.Start, r.End}
	if q.EntityType != "" {
		switch q.EntityType {
		case entityProduct, entityMachine, entitySlot, entityUser:
		default:
			return nil, fieldErrors{"entityType": "Jenis harus PRODUCT, MACHINE, SLOT atau USER."}.err()
		}
		where += " AND a.entity_type = ?"
		args = append(args, q.EntityType)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.created_at, a.user_id, u.name, a.action, a.entity_type, a.entity_id, a.entity_label, a.before_json, a.after_json
		FROM audit_logs a JOIN users u ON u.id = a.user_id
		WHERE `+where+`
		ORDER BY a.created_at DESC, a.id DESC
		LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditView{}
	for rows.Next() {
		var v AuditView
		var created sql.NullTime
		var before, after []byte
		if err := rows.Scan(&v.ID, &created, &v.User.ID, &v.User.Name, &v.Action, &v.EntityType, &v.EntityID, &v.EntityLabel, &before, &after); err != nil {
			return nil, err
		}
		v.CreatedAt = ms(created.Time)
		if before != nil {
			v.Before = json.RawMessage(before)
		}
		if after != nil {
			v.After = json.RawMessage(after)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
