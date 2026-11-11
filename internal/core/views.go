package core

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
