package httpapi

import "vending/internal/model"

// The wire shapes for the catalog endpoints, and the mappers from the domain.
// Field names here are the contract the Kiosk Agent and the kiosk page read
// (kiosk/README.md); testdata/contract.golden records them.

// productBriefDTO is a product as a slot shows it.
type productBriefDTO struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Price       int64   `json:"price"`
	ImageURL    *string `json:"imageUrl"`
}

func productBrief(p *model.Product) *productBriefDTO {
	if p == nil {
		return nil
	}
	return &productBriefDTO{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Price:       p.Price,
		ImageURL:    p.ImageURL,
	}
}

type machineInfoDTO struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type catalogSlotDTO struct {
	SlotNumber int              `json:"slotNumber"`
	Available  bool             `json:"available"`
	Product    *productBriefDTO `json:"product"`
}

type catalogDTO struct {
	CatalogVersion int64            `json:"catalogVersion"`
	Machine        machineInfoDTO   `json:"machine"`
	Slots          []catalogSlotDTO `json:"slots"`
}

func catalogView(c model.Catalog) catalogDTO {
	out := catalogDTO{
		CatalogVersion: c.CatalogVersion,
		Machine:        machineInfoDTO{Code: c.Machine.Code, Name: c.Machine.Name},
		Slots:          make([]catalogSlotDTO, 0, len(c.Slots)),
	}
	for _, s := range c.Slots {
		out.Slots = append(out.Slots, catalogSlotDTO{
			SlotNumber: s.SlotNumber,
			Available:  s.Available,
			Product:    productBrief(s.Product),
		})
	}
	return out
}

type heartbeatDTO struct {
	OK             bool  `json:"ok"`
	ServerTime     int64 `json:"serverTime"`
	CatalogVersion int64 `json:"catalogVersion"`
}

func heartbeatView(h model.Heartbeat) heartbeatDTO {
	return heartbeatDTO{OK: true, ServerTime: h.ServerTime, CatalogVersion: h.CatalogVersion}
}
