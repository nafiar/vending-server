package api_test

import "encoding/json"

// The wire shapes these tests decode into.
//
// They are declared here, and not imported from the server, on purpose. This
// suite is a black-box net over the HTTP contract the admin web and the Kiosk
// Agent are written against, so it has to be able to disagree with the server:
// a test that decodes into the server's own structs cannot notice a field being
// renamed, because it gets renamed on both sides at once. testdata/contract.golden
// guards the field names; these structs are what the assertions read.

type idNameRow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type machineRefRow struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type userRefRow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// auditRow is one row of GET /api/admin/audit-logs (UC-12, FR-26).
type auditRow struct {
	ID          int64           `json:"id"`
	CreatedAt   int64           `json:"createdAt"`
	User        idNameRow       `json:"user"`
	Action      string          `json:"action"`
	EntityType  string          `json:"entityType"`
	EntityID    int64           `json:"entityId"`
	EntityLabel string          `json:"entityLabel"`
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
}
