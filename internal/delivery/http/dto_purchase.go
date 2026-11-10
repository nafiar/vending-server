package http

import (
	"encoding/json"
	"time"

	"vending/internal/model"
	"vending/internal/usecase/purchase"
)

// The wire shapes for the purchase endpoints, and the mappers from the domain.
// Field names here are the contract the Kiosk Agent and the kiosk page read
// (kiosk/README.md); testdata/contract.golden records them.
//
// Every time on the wire is Unix milliseconds, and every one that the domain
// leaves unset is null rather than 0, so the kiosk can tell "not paid" from
// "paid at the epoch".

// createTransactionBody is the body of POST /api/machine/transactions. The
// numbers arrive as json.Number so that a string, a float or a missing field is
// refused here rather than silently read as zero.
type createTransactionBody struct {
	ID             string      `json:"id"`
	SlotNumber     json.Number `json:"slotNumber"`
	CatalogVersion json.Number `json:"catalogVersion"`
}

// input turns the body into what the usecase takes. The rules work in numbers;
// deciding that what arrived is a number at all is this layer's job (§7).
func (b createTransactionBody) input() (purchase.CreateInput, error) {
	slotNumber, ok1 := intField(b.SlotNumber)
	version, ok2 := intField(b.CatalogVersion)
	if !ok1 || !ok2 {
		return purchase.CreateInput{}, model.InvalidInput("slotNumber dan catalogVersion wajib berupa angka.", nil)
	}
	return purchase.CreateInput{ID: b.ID, SlotNumber: int(slotNumber), CatalogVersion: version}, nil
}

// resultBody is the agent's report of what the machine did.
type resultBody struct {
	Status        string `json:"status"`
	FailureReason string `json:"failureReason"`
}

// cancelBody says why a purchase ended: CANCEL (the customer tapped "Batal") or
// TIMEOUT (the kiosk's own 60 s ran out).
type cancelBody struct {
	Reason string `json:"reason"`
}

// createdTransactionDTO is a purchase just started, as the kiosk draws its
// payment screen from.
type createdTransactionDTO struct {
	ID               string          `json:"id"`
	SlotNumber       int             `json:"slotNumber"`
	Product          productBriefDTO `json:"product"`
	Amount           int64           `json:"amount"`
	ExpiresInSeconds int             `json:"expiresInSeconds"`
	Status           string          `json:"status"`
	CreatedAt        int64           `json:"createdAt"`
}

func createdTransactionView(p model.NewPurchase) createdTransactionDTO {
	t := p.Transaction
	return createdTransactionDTO{
		ID:         t.ID,
		SlotNumber: t.SlotNumber,
		Product:    *productBrief(&t.Product),
		Amount:     t.Price,
		// Seconds, truncated: a kiosk told 59 and given 60 is right to hurry.
		ExpiresInSeconds: int(p.ExpiresIn / time.Second),
		Status:           t.Status,
		CreatedAt:        t.CreatedAt.UnixMilli(),
	}
}

// transactionStateDTO is where a purchase has got to. It is what every step
// after the first answers with, and what the agent polls for after a restart.
type transactionStateDTO struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	FailureReason      *string `json:"failureReason"`
	PaidAt             *int64  `json:"paidAt"`
	FinishedAt         *int64  `json:"finishedAt"`
	CustomerReportedAt *int64  `json:"customerReportedAt"`
}

func transactionStateView(t model.Transaction) transactionStateDTO {
	return transactionStateDTO{
		ID:                 t.ID,
		Status:             t.Status,
		FailureReason:      t.FailureReason,
		PaidAt:             millis(t.PaidAt),
		FinishedAt:         millis(t.FinishedAt),
		CustomerReportedAt: millis(t.CustomerReportedAt),
	}
}

// reportResultDTO confirms a customer's "Produk tidak keluar?".
type reportResultDTO struct {
	ID         string `json:"id"`
	Reported   bool   `json:"reported"`
	ReportedAt int64  `json:"reportedAt"`
}

func reportResultView(t model.Transaction) reportResultDTO {
	out := reportResultDTO{ID: t.ID, Reported: t.CustomerReportedAt != nil}
	if t.CustomerReportedAt != nil {
		out.ReportedAt = t.CustomerReportedAt.UnixMilli()
	}
	return out
}

// millis is an optional time as the Unix milliseconds the API reports, or null.
func millis(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := t.UnixMilli()
	return &v
}
