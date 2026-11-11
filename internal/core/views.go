package core

import "encoding/json"

// JSON shapes returned by the API. Field names follow the contract the admin web
// (admin/README.md) and the kiosk (kiosk/README.md) already use. Every time is a
// Unix timestamp in milliseconds (UTC), which the browser turns into WIB.

// ---------------------------------------------------------------- shared

type IDName struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type MachineRef struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type UserRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// Totals is a revenue figure: the sum of price over DISPENSED transactions (BR-03).
type Totals struct {
	Revenue int64 `json:"revenue"`
	Sales   int64 `json:"sales"`
}

// ---------------------------------------------------------------- admin

type Summary struct {
	Today             Totals `json:"today"`
	SameTimeYesterday Totals `json:"sameTimeYesterday"`
	Last7             Totals `json:"last7"`
	Month             Totals `json:"month"`
}

type RecapTotals struct {
	Revenue   int64 `json:"revenue"`
	Sales     int64 `json:"sales"`
	Failed    int64 `json:"failed"`
	Reported  int64 `json:"reported"`
	Cancelled int64 `json:"cancelled"` // CANCELLED and EXPIRED
}

type SeriesPoint struct {
	Period  string `json:"period"` // 2026-10-22, 2026-10-19 (the Monday of the week) or 2026-10
	Start   int64  `json:"start"`
	Revenue int64  `json:"revenue"`
	Sales   int64  `json:"sales"`
}

type MachineRecap struct {
	MachineID  int64  `json:"machineId"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	IsActive   bool   `json:"isActive"`
	Revenue    int64  `json:"revenue"`
	Sales      int64  `json:"sales"`
	LastSaleAt *int64 `json:"lastSaleAt"`
	LastSeenAt *int64 `json:"lastSeenAt"`
}

type BreakdownRow struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Revenue int64  `json:"revenue"`
	Sales   int64  `json:"sales"`
}

type Recap struct {
	From        string         `json:"from"`
	To          string         `json:"to"`
	GroupBy     string         `json:"groupBy"`
	Breakdown   []BreakdownRow `json:"breakdown"`
	Totals      RecapTotals    `json:"totals"`
	Series      []SeriesPoint  `json:"series"`
	ByMachine   []MachineRecap `json:"byMachine"`
	TopProducts []BreakdownRow `json:"topProducts"`
}

type TransactionRow struct {
	ID                 string     `json:"id"`
	CreatedAt          int64      `json:"createdAt"`
	PaidAt             *int64     `json:"paidAt"`
	FinishedAt         *int64     `json:"finishedAt"`
	Machine            MachineRef `json:"machine"`
	SlotNumber         int        `json:"slotNumber"`
	Product            IDName     `json:"product"`
	Price              int64      `json:"price"`
	Status             string     `json:"status"`
	FailureReason      *string    `json:"failureReason"`
	CustomerReportedAt *int64     `json:"customerReportedAt"`
}

type TransactionPage struct {
	Total            int64            `json:"total"`
	Page             int              `json:"page"`
	PageSize         int              `json:"pageSize"`
	DispensedRevenue int64            `json:"dispensedRevenue"`
	DispensedCount   int64            `json:"dispensedCount"`
	Rows             []TransactionRow `json:"rows"`
}

type RestockItemView struct {
	SlotNumber     int     `json:"slotNumber"`
	ProductID      *int64  `json:"productId"`
	ProductName    *string `json:"productName"`
	QtyBefore      int     `json:"qtyBefore"`
	QtyAfter       int     `json:"qtyAfter"`
	ProductChanged bool    `json:"productChanged"`
}

type RestockSessionView struct {
	ID           int64             `json:"id"`
	ClientID     string            `json:"clientId"`
	StartedAt    int64             `json:"startedAt"`
	FinishedAt   int64             `json:"finishedAt"`
	Note         *string           `json:"note"`
	Machine      MachineRef        `json:"machine"`
	Operator     UserRef           `json:"operator"`
	ItemsAdded   int               `json:"itemsAdded"`
	SlotsChanged int               `json:"slotsChanged"`
	Items        []RestockItemView `json:"items"`
}

type RestockPage struct {
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
	Rows     []RestockSessionView `json:"rows"`
}

type AuditView struct {
	ID          int64           `json:"id"`
	CreatedAt   int64           `json:"createdAt"`
	User        IDName          `json:"user"`
	Action      string          `json:"action"`
	EntityType  string          `json:"entityType"`
	EntityID    int64           `json:"entityId"`
	EntityLabel string          `json:"entityLabel"`
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
}
