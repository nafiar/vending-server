// Package mysql is the products driver.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/product"
)

// MySQL reads and writes the products table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ product.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool or one transaction.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

const columns = `id, name, description, price, image_url, is_active, created_at, updated_at`

func scan(r mysqldb.RowScanner) (model.Product, error) {
	var p model.Product
	// description and image_url are nullable, and the times are read as
	// nullable because the demo seed writes rows directly.
	var desc, img sql.NullString
	var created, updated sql.NullTime
	err := r.Scan(&p.ID, &p.Name, &desc, &p.Price, &img, &p.IsActive, &created, &updated)
	p.Description = desc.String
	p.ImageURL = mysqldb.NullString(img)
	p.CreatedAt, p.UpdatedAt = created.Time, updated.Time
	return p, err
}

func (s *MySQL) Get(ctx context.Context, id int64) (model.Product, error) {
	p, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM products WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Product{}, model.ErrNoRecord
	}
	return p, err
}

func (s *MySQL) LockForUpdate(ctx context.Context, id int64) (model.Product, error) {
	p, err := scan(s.q.QueryRowContext(ctx, `SELECT `+columns+` FROM products WHERE id = ? FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Product{}, model.ErrNoRecord
	}
	return p, err
}

func (s *MySQL) List(ctx context.Context, includeArchived bool) ([]model.Product, error) {
	q := `SELECT ` + columns + ` FROM products`
	if !includeArchived {
		q += ` WHERE is_active`
	}
	// Active first, then by name: the order the admin's list is shown in.
	q += ` ORDER BY is_active DESC, name, id`
	rows, err := s.q.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Product{}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *MySQL) Create(ctx context.Context, p model.Product) (int64, error) {
	res, err := s.q.ExecContext(ctx, `INSERT INTO products
		(name, description, price, image_url, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, TRUE, ?, ?)`,
		p.Name, mysqldb.StrOrNil(p.Description), p.Price, p.ImageURL, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *MySQL) Update(ctx context.Context, p model.Product) error {
	var img any
	if p.ImageURL != nil {
		img = mysqldb.StrOrNil(*p.ImageURL)
	}
	_, err := s.q.ExecContext(ctx, `UPDATE products
		SET name = ?, description = ?, price = ?, image_url = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, mysqldb.StrOrNil(p.Description), p.Price, img, p.UpdatedAt, p.ID)
	return err
}

func (s *MySQL) SetActive(ctx context.Context, id int64, active bool, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE products SET is_active = ?, updated_at = ? WHERE id = ?`,
		active, at, id)
	return err
}

func (s *MySQL) Usage(ctx context.Context, productID int64) (map[int64][]model.SlotUse, error) {
	query := `SELECT s.product_id, s.next_product_id, s.slot_number, m.code
		FROM slots s JOIN machines m ON m.id = s.machine_id
		WHERE (s.product_id IS NOT NULL OR s.next_product_id IS NOT NULL)`
	var args []any
	if productID != 0 {
		query += ` AND (s.product_id = ? OR s.next_product_id = ?)`
		args = append(args, productID, productID)
	}
	query += ` ORDER BY m.code, s.slot_number`
	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]model.SlotUse{}
	for rows.Next() {
		var held, next sql.NullInt64
		var u model.SlotUse
		if err := rows.Scan(&held, &next, &u.SlotNumber, &u.MachineCode); err != nil {
			return nil, err
		}
		if held.Valid {
			out[held.Int64] = append(out[held.Int64], u)
		}
		if next.Valid {
			// The same slot, listed again as a plan rather than as a holding.
			planned := u
			planned.Planned = true
			out[next.Int64] = append(out[next.Int64], planned)
		}
	}
	return out, rows.Err()
}
