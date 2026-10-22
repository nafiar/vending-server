package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// queryer is what *sql.DB and *sql.Tx have in common.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ProductInput is the body of POST /admin/products and PUT /admin/products/{id}.
type ProductInput struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Price       json.Number `json:"price"`
	ImageURL    *string     `json:"imageUrl"` // a JPG/PNG data URL for a new image, the current URL to keep it, or null
}

const maxPrice = 100_000_000

type cleanProduct struct {
	name, description string
	price             int64
}

func (in ProductInput) validate(fe fieldErrors) cleanProduct {
	c := cleanProduct{name: strings.TrimSpace(in.Name), description: strings.TrimSpace(in.Description)}
	switch n := utf8.RuneCountInString(c.name); {
	case n == 0:
		fe.add("name", "Nama wajib diisi.")
	case n > 100:
		fe.add("name", "Nama paling panjang 100 karakter.")
	}
	if utf8.RuneCountInString(c.description) > 1000 {
		fe.add("description", "Deskripsi paling panjang 1000 karakter.")
	}
	price, err := strconv.ParseInt(in.Price.String(), 10, 64)
	switch {
	case err != nil || price <= 0:
		fe.add("price", "Harga harus bilangan bulat lebih dari 0.")
	case price > maxPrice:
		fe.add("price", "Harga paling besar Rp100.000.000.")
	}
	c.price = price
	return c
}

// imageChange is what a save does to the product image.
type imageChange struct {
	data []byte // a new upload
	ext  string
	keep bool // the request sent back the current URL
}

func readImageField(v *string, current string, fe fieldErrors) imageChange {
	if v == nil || strings.TrimSpace(*v) == "" {
		if current != "" {
			return imageChange{keep: true} // the admin web always resends the image; an empty value keeps it
		}
		return imageChange{}
	}
	if *v == current && current != "" {
		return imageChange{keep: true}
	}
	data, ext, msg := decodeImage(*v)
	if msg != "" {
		fe.add("image", msg)
		return imageChange{}
	}
	return imageChange{data: data, ext: ext}
}

// ListProducts returns every product with the slots that hold it or plan it (UC-07).
func (s *Service) ListProducts(ctx context.Context, includeArchived bool) ([]ProductView, error) {
	q := `SELECT id, name, description, price, image_url, is_active, created_at, updated_at FROM products`
	if !includeArchived {
		q += ` WHERE is_active`
	}
	q += ` ORDER BY is_active DESC, name, id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ProductView
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	used, err := s.productUsage(ctx, s.db, 0)
	if err != nil {
		return nil, err
	}
	out := make([]ProductView, 0, len(list))
	for _, p := range list {
		p.UsedIn = used[p.ID]
		if p.UsedIn == nil {
			p.UsedIn = []UsedIn{}
		}
		out = append(out, p)
	}
	return out, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanProduct(r rowScanner) (ProductView, error) {
	var p ProductView
	var desc, img sql.NullString
	var created, updated sql.NullTime
	if err := r.Scan(&p.ID, &p.Name, &desc, &p.Price, &img, &p.IsActive, &created, &updated); err != nil {
		return p, err
	}
	p.Description = desc.String
	p.ImageURL = nullString(img)
	p.CreatedAt = ms(created.Time)
	p.UpdatedAt = ms(updated.Time)
	return p, nil
}

// productUsage maps product id -> the slots holding or planning it. productID 0 means all products.
func (s *Service) productUsage(ctx context.Context, q queryer, productID int64) (map[int64][]UsedIn, error) {
	query := `SELECT s.product_id, s.next_product_id, s.slot_number, m.code
		FROM slots s JOIN machines m ON m.id = s.machine_id
		WHERE (s.product_id IS NOT NULL OR s.next_product_id IS NOT NULL)`
	var args []any
	if productID != 0 {
		query += ` AND (s.product_id = ? OR s.next_product_id = ?)`
		args = append(args, productID, productID)
	}
	query += ` ORDER BY m.code, s.slot_number`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]UsedIn{}
	for rows.Next() {
		var cur, next sql.NullInt64
		var u UsedIn
		if err := rows.Scan(&cur, &next, &u.SlotNumber, &u.MachineCode); err != nil {
			return nil, err
		}
		if cur.Valid {
			out[cur.Int64] = append(out[cur.Int64], u)
		}
		if next.Valid {
			u.Planned = true
			out[next.Int64] = append(out[next.Int64], u)
		}
	}
	return out, rows.Err()
}

func (s *Service) getProduct(ctx context.Context, q queryer, id int64) (ProductView, error) {
	p, err := scanProduct(q.QueryRowContext(ctx,
		`SELECT id, name, description, price, image_url, is_active, created_at, updated_at FROM products WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNotFound("Produk tidak ditemukan.")
	}
	if err != nil {
		return p, err
	}
	used, err := s.productUsage(ctx, q, id)
	if err != nil {
		return p, err
	}
	p.UsedIn = used[id]
	if p.UsedIn == nil {
		p.UsedIn = []UsedIn{}
	}
	return p, nil
}

// CreateProduct adds a product (FR-20). Without an image it gets a placeholder tile.
func (s *Service) CreateProduct(ctx context.Context, actor Principal, in ProductInput) (ProductView, error) {
	fe := fieldErrors{}
	c := in.validate(fe)
	img := readImageField(in.ImageURL, "", fe)
	if err := fe.err(); err != nil {
		return ProductView{}, err
	}
	var url string
	var err error
	if img.data != nil {
		url, err = s.storeImage(img.data, img.ext)
	} else {
		url, err = s.storeImage(placeholderSVG(c.name, utf8.RuneCountInString(c.name)), ".svg")
	}
	if err != nil {
		return ProductView{}, err
	}

	var id int64
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		now := s.now()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO products (name, description, price, image_url, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, TRUE, ?, ?)`, c.name, strOrNil(c.description), c.price, url, now, now)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionCreate, entityType: entityProduct, entityID: id, label: c.name,
			after: map[string]any{"name": c.name, "price": c.price},
		})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.getProduct(ctx, s.db, id)
}

// UpdateProduct edits a product. The change log gets the fields that changed,
// and every machine whose slots hold the product gets a new catalog version,
// so its kiosk can't sell at the old price (BR-09, TC-10).
func (s *Service) UpdateProduct(ctx context.Context, actor Principal, id int64, in ProductInput) (ProductView, error) {
	current, err := s.getProduct(ctx, s.db, id)
	if err != nil {
		return ProductView{}, err
	}
	curImage := ""
	if current.ImageURL != nil {
		curImage = *current.ImageURL
	}
	fe := fieldErrors{}
	c := in.validate(fe)
	img := readImageField(in.ImageURL, curImage, fe)
	if err := fe.err(); err != nil {
		return ProductView{}, err
	}
	newImage := curImage
	if img.data != nil {
		if newImage, err = s.storeImage(img.data, img.ext); err != nil {
			return ProductView{}, err
		}
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		var name, imgURL sql.NullString
		var desc sql.NullString
		var price int64
		err := tx.QueryRowContext(ctx, `SELECT name, description, price, image_url FROM products WHERE id = ? FOR UPDATE`, id).
			Scan(&name, &desc, &price, &imgURL)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("Produk tidak ditemukan.")
		}
		if err != nil {
			return err
		}
		before := map[string]any{"name": name.String, "description": desc.String, "price": price, "image": imgURL.String}
		after := map[string]any{"name": c.name, "description": c.description, "price": c.price, "image": newImage}
		b, a, changed := changedFields(before, after)
		if !changed {
			return nil
		}
		if _, ok := a["image"]; ok {
			b["image"], a["image"] = "(gambar lama)", "(gambar baru)"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET name = ?, description = ?, price = ?, image_url = ?, updated_at = ? WHERE id = ?`,
			c.name, strOrNil(c.description), c.price, strOrNil(newImage), s.now(), id); err != nil {
			return err
		}
		if err := s.bumpMachinesSelling(ctx, tx, id); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionUpdate, entityType: entityProduct, entityID: id, label: c.name,
			before: b, after: a,
		})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.getProduct(ctx, s.db, id)
}

// ArchiveProduct hides a product. It is refused while any slot holds or plans
// it (BR-10, TC-24); old transactions keep pointing at it (TC-25).
func (s *Service) ArchiveProduct(ctx context.Context, actor Principal, id int64) (ProductView, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var name string
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT name, is_active FROM products WHERE id = ? FOR UPDATE`, id).Scan(&name, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("Produk tidak ditemukan.")
		}
		if err != nil || !active {
			return err
		}
		used, err := s.productUsage(ctx, tx, id)
		if err != nil {
			return err
		}
		if slots := used[id]; len(slots) > 0 {
			labels := make([]string, len(slots))
			for i, u := range slots {
				labels[i] = u.MachineCode + " slot " + strconv.Itoa(u.SlotNumber)
			}
			return errConflict("CONFLICT", "Produk masih dipakai atau direncanakan di slot: "+strings.Join(labels, ", ")+".",
				map[string]any{"reason": "IN_USE", "slots": labels})
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET is_active = FALSE, updated_at = ? WHERE id = ?`, s.now(), id); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionArchive, entityType: entityProduct, entityID: id, label: name,
			before: map[string]any{"isActive": true}, after: map[string]any{"isActive": false},
		})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.getProduct(ctx, s.db, id)
}

// RestoreProduct brings an archived product back to the lists.
func (s *Service) RestoreProduct(ctx context.Context, actor Principal, id int64) (ProductView, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var name string
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT name, is_active FROM products WHERE id = ? FOR UPDATE`, id).Scan(&name, &active)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("Produk tidak ditemukan.")
		}
		if err != nil || active {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE products SET is_active = TRUE, updated_at = ? WHERE id = ?`, s.now(), id); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, auditEntry{
			actorID: actor.UserID, action: actionUpdate, entityType: entityProduct, entityID: id, label: name,
			before: map[string]any{"isActive": false}, after: map[string]any{"isActive": true},
		})
	})
	if err != nil {
		return ProductView{}, err
	}
	return s.getProduct(ctx, s.db, id)
}

// bumpMachinesSelling raises the catalog version of every machine whose slots
// hold the product. The slots are read without locks first, so this never
// holds slot locks while waiting for a machine row that a purchase holds.
func (s *Service) bumpMachinesSelling(ctx context.Context, tx *sql.Tx, productID int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT machine_id FROM slots WHERE product_id = ?`, productID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE machines SET catalog_version = catalog_version + 1 WHERE id IN (`+placeholders(len(ids))+`)`,
		int64sToArgs(ids)...)
	return err
}
