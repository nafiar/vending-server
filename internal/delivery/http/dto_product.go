package http

import (
	"encoding/json"

	"vending/internal/model"
	"vending/internal/usecase/product"
)

// The wire shapes for the products an admin manages, and the mappers from the
// domain. Field names here are the contract the admin web reads
// (admin/README.md); testdata/contract.golden records them.

// productBody is the body of POST and PUT /api/admin/products.
type productBody struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Price       json.Number `json:"price"`
	// ImageURL is a JPG or PNG data URL for a new picture, the current URL to
	// keep the one it has, or null for none.
	ImageURL *string `json:"imageUrl"`
}

// input turns the body into what the usecase takes. The picture travels as it
// arrived: what to do with it depends on what the product already has, which is
// the usecase's to know.
func (b productBody) input() product.Input {
	price, ok := intField(b.Price)
	return product.Input{
		Name:        b.Name,
		Description: b.Description,
		Price:       price,
		PriceOK:     ok,
		Image:       b.ImageURL,
	}
}

// slotUseDTO is one place a product is loaded or planned. The admin's product
// list shows these as the reason a product cannot be archived.
type slotUseDTO struct {
	MachineCode string `json:"machineCode"`
	SlotNumber  int    `json:"slotNumber"`
	Planned     bool   `json:"planned"`
}

// productDTO is a product as the admin's list and forms show it.
type productDTO struct {
	ID          int64        `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Price       int64        `json:"price"`
	ImageURL    *string      `json:"imageUrl"`
	IsActive    bool         `json:"isActive"`
	CreatedAt   int64        `json:"createdAt"`
	UpdatedAt   int64        `json:"updatedAt"`
	UsedIn      []slotUseDTO `json:"usedIn"`
}

func productView(d model.ProductDetail) productDTO {
	p := d.Product
	out := productDTO{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Price:       p.Price,
		ImageURL:    p.ImageURL,
		IsActive:    p.IsActive,
		CreatedAt:   p.CreatedAt.UnixMilli(),
		UpdatedAt:   p.UpdatedAt.UnixMilli(),
		UsedIn:      make([]slotUseDTO, 0, len(d.UsedIn)),
	}
	for _, u := range d.UsedIn {
		out.UsedIn = append(out.UsedIn, slotUseDTO{
			MachineCode: u.MachineCode, SlotNumber: u.SlotNumber, Planned: u.Planned,
		})
	}
	return out
}

func productViews(list []model.ProductDetail) []productDTO {
	out := make([]productDTO, 0, len(list))
	for _, d := range list {
		out = append(out, productView(d))
	}
	return out
}
