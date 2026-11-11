package http

import "vending/internal/model"

// The wire shape of the bookkeeping check (NFR-05). Field names here are the
// contract the admin web reads; testdata/contract.golden records them.

type slotReconciliationDTO struct {
	MachineCode    string `json:"machineCode"`
	SlotNumber     int    `json:"slotNumber"`
	LastVisitID    int64  `json:"lastVisitId"`
	LastVisitAt    int64  `json:"lastVisitAt"`
	CountAtVisit   int    `json:"countAtVisit"`
	SoldSince      int    `json:"soldSince"`
	Expected       int    `json:"expected"`
	Actual         int    `json:"actual"`
	Matches        bool   `json:"matches"`
	NeverRestocked bool   `json:"neverRestocked"`
}

type reconcileDTO struct {
	Slots      []slotReconciliationDTO `json:"slots"`
	Mismatches int                     `json:"mismatches"`
}

func reconcileView(r model.Reconciliation) reconcileDTO {
	out := reconcileDTO{Mismatches: r.Mismatches, Slots: make([]slotReconciliationDTO, 0, len(r.Slots))}
	for _, s := range r.Slots {
		out.Slots = append(out.Slots, slotReconciliationDTO{
			MachineCode:    s.MachineCode,
			SlotNumber:     s.SlotNumber,
			LastVisitID:    s.LastVisitID,
			LastVisitAt:    s.LastVisitAt,
			CountAtVisit:   s.CountAtVisit,
			SoldSince:      s.SoldSince,
			Expected:       s.Expected,
			Actual:         s.Actual,
			Matches:        s.Matches,
			NeverRestocked: s.NeverRestocked,
		})
	}
	return out
}
