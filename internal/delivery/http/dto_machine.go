package http

import (
	"encoding/json"

	"vending/internal/model"
	machineuc "vending/internal/usecase/machine"
)

// The wire shapes for the machines an admin registers, and the mappers from the
// domain. Field names here are the contract the admin web reads
// (admin/README.md); testdata/contract.golden records them.

// machineBody is the body of POST /api/admin/machines.
type machineBody struct {
	Code      string      `json:"code"`
	Name      string      `json:"name"`
	Location  string      `json:"location"`
	SlotCount json.Number `json:"slotCount"`
	Capacity  json.Number `json:"capacity"`
}

// input turns the body into what the usecase takes. A number this layer cannot
// read is passed on as 0, which fails the usecase's range check with the
// message any other impossible value gets.
func (b machineBody) input() machineuc.Input {
	slotCount, _ := intField(b.SlotCount)
	capacity, _ := intField(b.Capacity)
	return machineuc.Input{
		Code:      b.Code,
		Name:      b.Name,
		Location:  b.Location,
		SlotCount: int(slotCount),
		Capacity:  int(capacity),
	}
}

// slotBody is the body of PUT /api/admin/machines/{id}/slots/{n}.
type slotBody struct {
	Capacity json.Number `json:"capacity"`
	// NextProductID is null, or absent, for no planned change.
	NextProductID *json.Number `json:"nextProductId"`
}

func (b slotBody) input() machineuc.SlotInput {
	capacity, _ := intField(b.Capacity)
	in := machineuc.SlotInput{Capacity: int(capacity)}
	// An explicit null and an empty string both mean "no plan"; anything else
	// is meant as a product id, and a value that is not one arrives as 0 so the
	// usecase refuses it as a product that does not exist.
	if b.NextProductID != nil && b.NextProductID.String() != "" {
		id, _ := intField(*b.NextProductID)
		in.NextProductID = &id
	}
	return in
}

// machineDTO is a machine as the admin's list and detail screens show it.
//
// slots is omitted from the list, where it would multiply the response by the
// number of coils for nothing: the list shows counts, not coils.
type machineDTO struct {
	ID             int64     `json:"id"`
	Code           string    `json:"code"`
	Name           string    `json:"name"`
	Location       string    `json:"location"`
	IsActive       bool      `json:"isActive"`
	CatalogVersion int64     `json:"catalogVersion"`
	CreatedAt      int64     `json:"createdAt"`
	LastSeenAt     *int64    `json:"lastSeenAt"`
	LastSaleAt     *int64    `json:"lastSaleAt"`
	SlotCount      int       `json:"slotCount"`
	EmptySlots     int       `json:"emptySlots"`
	BlockedSlots   int       `json:"blockedSlots"`
	MonthRevenue   int64     `json:"monthRevenue"`
	MonthSales     int64     `json:"monthSales"`
	Slots          []slotDTO `json:"slots,omitempty"`
}

func machineView(d model.MachineDetail) machineDTO {
	m := d.Machine
	out := machineDTO{
		ID:             m.ID,
		Code:           m.Code,
		Name:           m.Name,
		Location:       m.Location,
		IsActive:       m.IsActive,
		CatalogVersion: m.CatalogVersion,
		CreatedAt:      m.CreatedAt.UnixMilli(),
		LastSeenAt:     millis(m.LastSeenAt),
		LastSaleAt:     millis(d.LastSaleAt),
		SlotCount:      d.Slots.Count,
		EmptySlots:     d.Slots.Empty,
		BlockedSlots:   d.Slots.Blocked,
		MonthRevenue:   d.Month.Revenue,
		MonthSales:     d.Month.Sales,
	}
	for _, sl := range d.Coils {
		out.Slots = append(out.Slots, slotView(sl))
	}
	return out
}

func machineViews(list []model.MachineDetail) []machineDTO {
	out := make([]machineDTO, 0, len(list))
	for _, d := range list {
		out = append(out, machineView(d))
	}
	return out
}

// machineWithKeyDTO carries the machine key, which the server will never be
// able to show again: only its hash is kept.
type machineWithKeyDTO struct {
	Machine machineDTO `json:"machine"`
	APIKey  string     `json:"apiKey"`
}

func machineWithKeyView(k model.MachineWithKey) machineWithKeyDTO {
	return machineWithKeyDTO{Machine: machineView(k.Machine), APIKey: k.APIKey}
}
