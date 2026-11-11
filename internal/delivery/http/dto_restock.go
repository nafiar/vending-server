package http

import (
	"encoding/json"

	"vending/internal/model"
	"vending/internal/usecase/restock"
)

// The wire shapes for an operator's visit, and the mappers from the domain.
// Field names here are the contract the kiosk's restock screen reads
// (kiosk/README.md); testdata/contract.golden records them.

// restockBody is the body the kiosk sends when the operator confirms a visit.
type restockBody struct {
	// ID is the kiosk's own id for this visit; a resend with the same id is not
	// saved twice.
	ID string `json:"id"`
	// FinishedAt is optional, and may be ISO 8601 (as the kiosk sends it) or
	// Unix milliseconds (as a journaled visit carries it).
	FinishedAt json.RawMessage   `json:"finishedAt"`
	Note       string            `json:"note"`
	Items      []restockItemBody `json:"items"`
}

// restockItemBody is one slot of a visit. qtyBefore, if the client sends it, is
// ignored: the server's own count is the one the record is built from.
type restockItemBody struct {
	SlotNumber json.Number `json:"slotNumber"`
	// QtyAfter is read as raw JSON so that "", "3", 2.5 and null are each
	// refused as a bad count rather than as unreadable JSON: the operator typed
	// something, and the answer should name the slot they typed it in.
	QtyAfter json.RawMessage `json:"qtyAfter"`
}

// input turns the body into what the usecase takes.
//
// A value this layer cannot read is passed on as one the rules cannot use: an
// unreadable slot number as 0 and an unreadable count as -1. That keeps the
// judgement in one place — the usecase refuses both with the message it would
// give any other impossible value — rather than splitting it between the two
// layers and risking two different answers for the same mistake.
func (b restockBody) input() restock.SaveInput {
	in := restock.SaveInput{ID: b.ID, Note: b.Note, Items: make([]restock.ItemInput, 0, len(b.Items))}
	if t, ok := rawTime(b.FinishedAt); ok {
		in.FinishedAt = &t
	}
	for _, it := range b.Items {
		item := restock.ItemInput{Label: it.SlotNumber.String(), QtyAfter: -1}
		if n, ok := intField(it.SlotNumber); ok {
			item.SlotNumber = int(n)
		}
		if q, ok := rawInt(it.QtyAfter); ok {
			item.QtyAfter = int(q)
		}
		in.Items = append(in.Items, item)
	}
	return in
}

// slotDTO is one slot as the restock screen and the admin's machine page show
// it: what is in it, how much fits, and what is planned for it.
type slotDTO struct {
	SlotNumber  int              `json:"slotNumber"`
	Capacity    int              `json:"capacity"`
	Qty         int              `json:"qty"`
	NeedsCheck  bool             `json:"needsCheck"`
	Product     *productBriefDTO `json:"product"`
	NextProduct *productBriefDTO `json:"nextProduct"`
}

func slotView(s model.Slot) slotDTO {
	return slotDTO{
		SlotNumber:  s.SlotNumber,
		Capacity:    s.Capacity,
		Qty:         s.Qty,
		NeedsCheck:  s.NeedsCheck,
		Product:     productBrief(s.Product),
		NextProduct: productBrief(s.NextProduct),
	}
}

// operatorSlotsDTO is the whole restock screen (FR-14).
type operatorSlotsDTO struct {
	Machine machineInfoDTO `json:"machine"`
	Slots   []slotDTO      `json:"slots"`
}

func operatorSlotsView(ms model.MachineSlots) operatorSlotsDTO {
	out := operatorSlotsDTO{
		Machine: machineInfoDTO{Code: ms.Machine.Code, Name: ms.Machine.Name},
		Slots:   make([]slotDTO, 0, len(ms.Slots)),
	}
	for _, s := range ms.Slots {
		out.Slots = append(out.Slots, slotView(s))
	}
	return out
}

// restockSavedDTO confirms a saved visit. catalogVersion is what the kiosk
// reloads at, so it can go straight back to selling.
type restockSavedDTO struct {
	ID             string `json:"id"`
	SessionID      int64  `json:"sessionId"`
	SavedAt        int64  `json:"savedAt"`
	CatalogVersion int64  `json:"catalogVersion"`
}

func restockSavedView(r model.RestockSaved) restockSavedDTO {
	return restockSavedDTO{
		ID:             r.ClientID,
		SessionID:      r.SessionID,
		SavedAt:        r.SavedAt.UnixMilli(),
		CatalogVersion: r.CatalogVersion,
	}
}
