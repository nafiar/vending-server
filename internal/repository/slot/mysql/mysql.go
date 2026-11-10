// Package mysql is the slots driver.
package mysql

import (
	"context"
	"database/sql"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/slot"
)

// MySQL reads and writes the slots table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ slot.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool or one transaction.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

// listQuery joins each slot to the product it holds and the one planned for it.
// Both are optional, so both joins are left joins.
const listQuery = `
	SELECT s.id, s.machine_id, s.slot_number, s.capacity, s.qty, s.needs_check,
	       p.id, p.name, p.description, p.price, p.image_url, p.is_active,
	       n.id, n.name, n.description, n.price, n.image_url, n.is_active
	FROM slots s
	LEFT JOIN products p ON p.id = s.product_id
	LEFT JOIN products n ON n.id = s.next_product_id
	WHERE s.machine_id = ?
	ORDER BY s.slot_number`

func (s *MySQL) ListByMachine(ctx context.Context, machineID int64) ([]model.Slot, error) {
	rows, err := s.q.QueryContext(ctx, listQuery, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Slot{}
	for rows.Next() {
		sl, err := scanSlot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sl)
	}
	return out, rows.Err()
}

// nullProduct is a product reached through a left join: every column may be NULL.
type nullProduct struct {
	id          sql.NullInt64
	name        sql.NullString
	description sql.NullString
	price       sql.NullInt64
	imageURL    sql.NullString
	isActive    sql.NullBool
}

// product returns the product, or nil when the join found none.
func (p nullProduct) product() *model.Product {
	if !p.id.Valid {
		return nil
	}
	return &model.Product{
		ID:          p.id.Int64,
		Name:        p.name.String,
		Description: p.description.String,
		Price:       p.price.Int64,
		ImageURL:    mysqldb.NullString(p.imageURL),
		IsActive:    p.isActive.Bool,
	}
}

func scanSlot(r mysqldb.RowScanner) (model.Slot, error) {
	var s model.Slot
	var held, next nullProduct
	err := r.Scan(&s.ID, &s.MachineID, &s.SlotNumber, &s.Capacity, &s.Qty, &s.NeedsCheck,
		&held.id, &held.name, &held.description, &held.price, &held.imageURL, &held.isActive,
		&next.id, &next.name, &next.description, &next.price, &next.imageURL, &next.isActive)
	s.Product, s.NextProduct = held.product(), next.product()
	return s, err
}
