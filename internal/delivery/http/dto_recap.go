package http

import (
	"vending/internal/model"
	"vending/internal/usecase/recap"
)

// The wire shapes for the revenue figures, and the mappers from the domain.
// Field names here are the contract the admin web reads (admin/README.md);
// testdata/contract.golden records them.

// totalsDTO is a revenue figure: what came in and how many sales it was.
type totalsDTO struct {
	Revenue int64 `json:"revenue"`
	Sales   int64 `json:"sales"`
}

func totalsView(t model.Totals) totalsDTO {
	return totalsDTO{Revenue: t.Revenue, Sales: t.Sales}
}

// summaryDTO is the four tiles at the top of the admin home (FR-17).
type summaryDTO struct {
	Today             totalsDTO `json:"today"`
	SameTimeYesterday totalsDTO `json:"sameTimeYesterday"`
	Last7             totalsDTO `json:"last7"`
	Month             totalsDTO `json:"month"`
}

func summaryView(s model.Summary) summaryDTO {
	return summaryDTO{
		Today:             totalsView(s.Today),
		SameTimeYesterday: totalsView(s.SameTimeYesterday),
		Last7:             totalsView(s.Last7),
		Month:             totalsView(s.Month),
	}
}

// recapQuery is the query string of GET /api/admin/recap.
func recapQuery(q *query) recap.Query {
	return recap.Query{
		From:      q.str("from"),
		To:        q.str("to"),
		MachineID: q.int("machineId"),
		GroupBy:   q.str("groupBy"),
		Breakdown: q.str("breakdown"),
	}
}

type recapTotalsDTO struct {
	Revenue   int64 `json:"revenue"`
	Sales     int64 `json:"sales"`
	Failed    int64 `json:"failed"`
	Reported  int64 `json:"reported"`
	Cancelled int64 `json:"cancelled"`
}

type seriesPointDTO struct {
	Period  string `json:"period"`
	Start   int64  `json:"start"`
	Revenue int64  `json:"revenue"`
	Sales   int64  `json:"sales"`
}

type machineRecapDTO struct {
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

type breakdownRowDTO struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Revenue int64  `json:"revenue"`
	Sales   int64  `json:"sales"`
}

type recapDTO struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	GroupBy     string            `json:"groupBy"`
	Breakdown   []breakdownRowDTO `json:"breakdown"`
	Totals      recapTotalsDTO    `json:"totals"`
	Series      []seriesPointDTO  `json:"series"`
	ByMachine   []machineRecapDTO `json:"byMachine"`
	TopProducts []breakdownRowDTO `json:"topProducts"`
}

func recapView(r model.Recap) recapDTO {
	out := recapDTO{
		From:    r.From,
		To:      r.To,
		GroupBy: r.GroupBy,
		Totals: recapTotalsDTO{
			Revenue:   r.Totals.Revenue,
			Sales:     r.Totals.Sales,
			Failed:    r.Totals.Failed,
			Reported:  r.Totals.Reported,
			Cancelled: r.Totals.Cancelled,
		},
		Breakdown:   breakdownRows(r.Breakdown),
		TopProducts: breakdownRows(r.TopProducts),
		ByMachine:   make([]machineRecapDTO, 0, len(r.ByMachine)),
	}
	for _, p := range r.Series {
		out.Series = append(out.Series, seriesPointDTO{
			Period: p.Period, Start: p.Start.UnixMilli(),
			Revenue: p.Totals.Revenue, Sales: p.Totals.Sales,
		})
	}
	for _, m := range r.ByMachine {
		out.ByMachine = append(out.ByMachine, machineRecapDTO{
			MachineID: m.MachineID, Code: m.Code, Name: m.Name, Location: m.Location,
			IsActive: m.IsActive, Revenue: m.Totals.Revenue, Sales: m.Totals.Sales,
			LastSaleAt: millis(m.LastSaleAt), LastSeenAt: millis(m.LastSeenAt),
		})
	}
	return out
}

func breakdownRows(list []model.BreakdownRow) []breakdownRowDTO {
	out := make([]breakdownRowDTO, 0, len(list))
	for _, b := range list {
		out = append(out, breakdownRowDTO{Key: b.Key, Label: b.Label, Revenue: b.Revenue, Sales: b.Sales})
	}
	return out
}

// ---------------------------------------------------------------- history

// historyQuery is the query string of GET /api/admin/transactions.
func historyQuery(q *query) recap.HistoryQuery {
	return recap.HistoryQuery{
		From:       q.str("from"),
		To:         q.str("to"),
		MachineID:  q.int("machineId"),
		SlotNumber: int(q.int("slotNumber")),
		Status:     q.str("status"),
		Flag:       q.str("flag"),
		Page:       int(q.int("page")),
		PageSize:   int(q.int("pageSize")),
		All:        q.bool("all"),
	}
}

type transactionRowDTO struct {
	ID                 string        `json:"id"`
	CreatedAt          int64         `json:"createdAt"`
	PaidAt             *int64        `json:"paidAt"`
	FinishedAt         *int64        `json:"finishedAt"`
	Machine            machineRefDTO `json:"machine"`
	SlotNumber         int           `json:"slotNumber"`
	Product            idNameDTO     `json:"product"`
	Price              int64         `json:"price"`
	Status             string        `json:"status"`
	FailureReason      *string       `json:"failureReason"`
	CustomerReportedAt *int64        `json:"customerReportedAt"`
}

type transactionPageDTO struct {
	Total            int64               `json:"total"`
	Page             int                 `json:"page"`
	PageSize         int                 `json:"pageSize"`
	DispensedRevenue int64               `json:"dispensedRevenue"`
	DispensedCount   int64               `json:"dispensedCount"`
	Rows             []transactionRowDTO `json:"rows"`
}

func historyView(h model.TransactionHistory) transactionPageDTO {
	out := transactionPageDTO{
		Total:            h.Total,
		Page:             h.Page,
		PageSize:         h.PageSize,
		DispensedRevenue: h.DispensedRevenue,
		DispensedCount:   h.DispensedCount,
		Rows:             make([]transactionRowDTO, 0, len(h.Rows)),
	}
	for _, r := range h.Rows {
		out.Rows = append(out.Rows, transactionRowDTO{
			ID:                 r.ID,
			CreatedAt:          r.CreatedAt.UnixMilli(),
			PaidAt:             millis(r.PaidAt),
			FinishedAt:         millis(r.FinishedAt),
			Machine:            machineRefDTO{ID: r.MachineID, Code: r.MachineCode, Name: r.MachineName},
			SlotNumber:         r.SlotNumber,
			Product:            idNameDTO{ID: r.ProductID, Name: r.ProductName},
			Price:              r.Price,
			Status:             r.Status,
			FailureReason:      r.FailureReason,
			CustomerReportedAt: millis(r.CustomerReportedAt),
		})
	}
	return out
}
