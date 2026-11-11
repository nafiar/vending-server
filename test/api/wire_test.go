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

// transactionRow is one row of GET /api/admin/transactions (UC-09, FR-24).
type transactionRow struct {
	ID                 string        `json:"id"`
	CreatedAt          int64         `json:"createdAt"`
	PaidAt             *int64        `json:"paidAt"`
	FinishedAt         *int64        `json:"finishedAt"`
	Machine            machineRefRow `json:"machine"`
	SlotNumber         int           `json:"slotNumber"`
	Product            idNameRow     `json:"product"`
	Price              int64         `json:"price"`
	Status             string        `json:"status"`
	FailureReason      *string       `json:"failureReason"`
	CustomerReportedAt *int64        `json:"customerReportedAt"`
}

// transactionPage is GET /api/admin/transactions: one page, and the figures for
// the whole filtered set.
type transactionPage struct {
	Total            int64            `json:"total"`
	Page             int              `json:"page"`
	PageSize         int              `json:"pageSize"`
	DispensedRevenue int64            `json:"dispensedRevenue"`
	DispensedCount   int64            `json:"dispensedCount"`
	Rows             []transactionRow `json:"rows"`
}
