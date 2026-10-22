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

// ProductBrief is a product as a slot shows it.
type ProductBrief struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       int64   `json:"price"`
	ImageURL    *string `json:"imageUrl"`
}

// Totals is a revenue figure: the sum of price over DISPENSED transactions (BR-03).
type Totals struct {
	Revenue int64 `json:"revenue"`
	Sales   int64 `json:"sales"`
}

// ---------------------------------------------------------------- admin

type LoginResult struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type Me struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

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

type UsedIn struct {
	MachineCode string `json:"machineCode"`
	SlotNumber  int    `json:"slotNumber"`
	Planned     bool   `json:"planned"` // true: the product is the slot's planned next product
}

type ProductView struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Price       int64    `json:"price"`
	ImageURL    *string  `json:"imageUrl"`
	IsActive    bool     `json:"isActive"`
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
	UsedIn      []UsedIn `json:"usedIn"`
}

type SlotView struct {
	SlotNumber  int           `json:"slotNumber"`
	Capacity    int           `json:"capacity"`
	Qty         int           `json:"qty"`
	NeedsCheck  bool          `json:"needsCheck"`
	Product     *ProductBrief `json:"product"`
	NextProduct *ProductBrief `json:"nextProduct"`
}

type MachineView struct {
	ID             int64      `json:"id"`
	Code           string     `json:"code"`
	Name           string     `json:"name"`
	Location       string     `json:"location"`
	IsActive       bool       `json:"isActive"`
	CatalogVersion int64      `json:"catalogVersion"`
	CreatedAt      int64      `json:"createdAt"`
	LastSeenAt     *int64     `json:"lastSeenAt"`
	LastSaleAt     *int64     `json:"lastSaleAt"`
	SlotCount      int        `json:"slotCount"`
	EmptySlots     int        `json:"emptySlots"`
	BlockedSlots   int        `json:"blockedSlots"`
	MonthRevenue   int64      `json:"monthRevenue"`
	MonthSales     int64      `json:"monthSales"`
	Slots          []SlotView `json:"slots,omitempty"`
}

type MachineWithKey struct {
	Machine MachineView `json:"machine"`
	APIKey  string      `json:"apiKey"`
}

type OperatorView struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Username         string  `json:"username"`
	Role             string  `json:"role"`
	IsActive         bool    `json:"isActive"`
	CreatedAt        int64   `json:"createdAt"`
	LastVisitAt      *int64  `json:"lastVisitAt"`
	LastVisitMachine *string `json:"lastVisitMachine"`
	Visits30         int64   `json:"visits30"`
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

// ---------------------------------------------------------------- machine (Kiosk Agent)

type MachineInfo struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type CatalogSlot struct {
	SlotNumber int           `json:"slotNumber"`
	Available  bool          `json:"available"`
	Product    *ProductBrief `json:"product"`
}

type Catalog struct {
	CatalogVersion int64         `json:"catalogVersion"`
	Machine        MachineInfo   `json:"machine"`
	Slots          []CatalogSlot `json:"slots"`
}

type CreatedTransaction struct {
	ID               string       `json:"id"`
	SlotNumber       int          `json:"slotNumber"`
	Product          ProductBrief `json:"product"`
	Amount           int64        `json:"amount"`
	ExpiresInSeconds int          `json:"expiresInSeconds"`
	Status           string       `json:"status"`
	CreatedAt        int64        `json:"createdAt"`
}

type TransactionState struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	FailureReason      *string `json:"failureReason"`
	PaidAt             *int64  `json:"paidAt"`
	FinishedAt         *int64  `json:"finishedAt"`
	CustomerReportedAt *int64  `json:"customerReportedAt"`
}

type ReportResult struct {
	ID         string `json:"id"`
	Reported   bool   `json:"reported"`
	ReportedAt int64  `json:"reportedAt"`
}

type OperatorSlots struct {
	Machine MachineInfo `json:"machine"`
	Slots   []SlotView  `json:"slots"`
}

type RestockSaved struct {
	ID             string `json:"id"`
	SessionID      int64  `json:"sessionId"`
	SavedAt        int64  `json:"savedAt"`
	CatalogVersion int64  `json:"catalogVersion"`
}

type HeartbeatResult struct {
	OK             bool  `json:"ok"`
	ServerTime     int64 `json:"serverTime"`
	CatalogVersion int64 `json:"catalogVersion"`
}
