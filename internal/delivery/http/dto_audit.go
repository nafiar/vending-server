package http

import (
	"encoding/json"

	"vending/internal/model"
	"vending/internal/usecase/audit"
)

// The wire shape of the change log, and the mapper from the domain. Field names
// here are the contract the admin web reads (admin/README.md);
// testdata/contract.golden records them.

// auditQuery is the query string of GET /api/admin/audit-logs.
func auditQuery(q *query) audit.Query {
	return audit.Query{
		From:       q.str("from"),
		To:         q.str("to"),
		EntityType: q.str("entityType"),
	}
}

type auditDTO struct {
	ID          int64           `json:"id"`
	CreatedAt   int64           `json:"createdAt"`
	User        idNameDTO       `json:"user"`
	Action      string          `json:"action"`
	EntityType  string          `json:"entityType"`
	EntityID    int64           `json:"entityId"`
	EntityLabel string          `json:"entityLabel"`
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
}

// auditView maps the rows, and never re-encodes Before and After: they are the
// JSON the change was logged with, and they go out as they were stored. A nil
// one is the JSON null the admin web already expects for a CREATE.
func auditView(logs []model.AuditLog) []auditDTO {
	out := make([]auditDTO, 0, len(logs))
	for _, e := range logs {
		out = append(out, auditDTO{
			ID:          e.ID,
			CreatedAt:   e.At.UnixMilli(),
			User:        idNameDTO{ID: e.ActorID, Name: e.ActorName},
			Action:      e.Action,
			EntityType:  e.EntityType,
			EntityID:    e.EntityID,
			EntityLabel: e.EntityLabel,
			Before:      json.RawMessage(e.Before),
			After:       json.RawMessage(e.After),
		})
	}
	return out
}
