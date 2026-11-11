// Package product owns the products an admin manages (UC-07, FR-19, FR-20).
//
// Three rules shape everything here.
//
// A product is archived, never deleted (BR-10, TC-24, TC-25). Past transactions
// point at it, and a sale that has lost what it sold is not a record of
// anything. Archiving is refused while any slot still holds or plans it, so a
// kiosk can never be left offering something that is no longer on the list.
//
// Editing a product changes what every machine holding it is selling, so each
// of those machines gets a new catalog version and its kiosk reloads. Without
// that, a screen drawn before the edit would go on quoting the old price, and
// the purchase rules would refuse the sale as stale with nothing to show the
// customer (BR-09, TC-10).
//
// Every change is written to the change log in the same unit of work as the
// change itself, which is what makes FR-26's "the log can never miss a change"
// true rather than merely intended.
package product

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"vending/internal/common/clock"
	"vending/internal/common/imagex"
	"vending/internal/model"
	"vending/internal/repository/imagestore"
	"vending/internal/repository/product"
	"vending/internal/repository/uow"
)

//go:generate mockgen -source=product.go -destination=mock/product_mock.go -package=mock

// Input is a product an admin is saving. The price is already a number:
// deciding that what arrived is a number at all is the delivery layer's work.
type Input struct {
	Name        string
	Description string
	Price       int64
	// PriceOK is false when what arrived was not a whole number at all, which
	// fails the same check as a price of zero and with the same message.
	PriceOK bool
	// Image is the picture field as it arrived: a data URL for a new picture,
	// the URL the product already has to keep that one, or nil for none.
	//
	// What to do with it is decided here rather than in delivery, because it
	// depends on what the product currently has, and this is the layer that
	// reads that. common/imagex does the decoding and says what is wrong with a
	// picture it cannot use.
	Image *string
}

// imageChoice is what a save does about the picture: store these bytes, leave
// the current one alone, or draw a placeholder.
type imageChoice struct {
	data []byte
	ext  string
	keep bool
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// List returns the products with everywhere each is used (UC-07).
	List(ctx context.Context, includeArchived bool) ([]model.ProductDetail, error)
	// Create adds a product (FR-20).
	Create(ctx context.Context, actor model.Principal, in Input) (model.ProductDetail, error)
	// Update edits one (FR-19).
	Update(ctx context.Context, actor model.Principal, id int64, in Input) (model.ProductDetail, error)
	// Archive hides a product, if nothing is still selling it (BR-10).
	Archive(ctx context.Context, actor model.Principal, id int64) (model.ProductDetail, error)
	// Restore brings an archived product back.
	Restore(ctx context.Context, actor model.Principal, id int64) (model.ProductDetail, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	uow uow.Repository
	// products is bound to the pool, for the reads that change nothing.
	products product.Repository
	images   imagestore.Repository
	clock    clock.Clock
	log      *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(u uow.Repository, products product.Repository, images imagestore.Repository, c clock.Clock, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uow: u, products: products, images: images, clock: c, log: log}
}

func errNotFound() *model.Error { return model.NotFound("Produk tidak ditemukan.") }

// clean checks what an admin typed and returns it tidied up, adding a message
// per field at fault to fe. Every problem is collected rather than returned at
// the first, so a form can mark all of them at once.
func (in Input) clean(fe model.FieldErrors) (name, description string, price int64) {
	name = strings.TrimSpace(in.Name)
	description = strings.TrimSpace(in.Description)

	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe.Add("name", "Nama wajib diisi.")
	case n > model.MaxProductNameRunes:
		fe.Add("name", "Nama paling panjang 100 karakter.")
	}
	if utf8.RuneCountInString(description) > model.MaxProductDescriptionRunes {
		fe.Add("description", "Deskripsi paling panjang 1000 karakter.")
	}
	switch {
	case !in.PriceOK || in.Price <= 0:
		fe.Add("price", "Harga harus bilangan bulat lebih dari 0.")
	case in.Price > model.MaxPrice:
		fe.Add("price", "Harga paling besar Rp100.000.000.")
	}
	return name, description, in.Price
}

// readImage decides which of the three picture cases this save is, against the
// picture the product has now (empty when it has none, or is being created).
//
// A problem with the picture goes into the same field errors as the rest, so a
// form gets one answer marking every field at fault rather than one per save.
func readImage(sent *string, current string, fe model.FieldErrors) imageChoice {
	if sent == nil || strings.TrimSpace(*sent) == "" {
		// The admin web always sends the picture back, so an empty value on a
		// product that has one means "leave it alone", not "remove it".
		return imageChoice{keep: current != ""}
	}
	if *sent == current && current != "" {
		// The same URL came back: storing it again would mint a second copy
		// under a new name for no reason.
		return imageChoice{keep: true}
	}
	data, ext, msg := imagex.Decode(*sent)
	if msg != "" {
		fe.Add("image", msg)
		return imageChoice{}
	}
	return imageChoice{data: data, ext: ext}
}

// List returns every product with the slots using it.
func (s *Service) List(ctx context.Context, includeArchived bool) ([]model.ProductDetail, error) {
	products, err := s.products.List(ctx, includeArchived)
	if err != nil {
		return nil, err
	}
	// One query for all of the usage rather than one per product: the admin's
	// list is the screen most often opened, and this is the whole of it.
	used, err := s.products.Usage(ctx, 0)
	if err != nil {
		return nil, err
	}
	out := make([]model.ProductDetail, 0, len(products))
	for _, p := range products {
		out = append(out, model.ProductDetail{Product: p, UsedIn: usedOrEmpty(used[p.ID])})
	}
	return out, nil
}

// usedOrEmpty is never nil, so the wire shape carries [] rather than null: the
// admin web iterates it without checking.
func usedOrEmpty(u []model.SlotUse) []model.SlotUse {
	if u == nil {
		return []model.SlotUse{}
	}
	return u
}

// detail reads one product back with its usage, which is what every write
// answers with.
func (s *Service) detail(ctx context.Context, id int64) (model.ProductDetail, error) {
	p, err := s.products.Get(ctx, id)
	if errors.Is(err, model.ErrNoRecord) {
		return model.ProductDetail{}, errNotFound()
	}
	if err != nil {
		return model.ProductDetail{}, err
	}
	used, err := s.products.Usage(ctx, id)
	if err != nil {
		return model.ProductDetail{}, err
	}
	return model.ProductDetail{Product: p, UsedIn: usedOrEmpty(used[id])}, nil
}

// Create adds a product. Without a picture it gets a placeholder tile, so the
// kiosk never shows an empty square.
func (s *Service) Create(ctx context.Context, actor model.Principal, in Input) (model.ProductDetail, error) {
	fe := model.FieldErrors{}
	name, description, price := in.clean(fe)
	img := readImage(in.Image, "", fe)
	if err := fe.Err(); err != nil {
		return model.ProductDetail{}, err
	}

	// The picture is stored before the unit of work opens. Writing a file is
	// not something a database transaction can roll back, so it must not be
	// done while holding locks: the worst case here is an unreferenced file,
	// which costs disk and nothing else.
	url, err := s.storeImage(img, name)
	if err != nil {
		return model.ProductDetail{}, err
	}

	var id int64
	err = s.uow.Do(ctx, func(tx uow.Tx) error {
		id = 0
		now := s.clock.Now()
		id, err = tx.Products().Create(ctx, model.Product{
			Name: name, Description: description, Price: price, ImageURL: &url,
			CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		// The log row commits with the product or not at all (FR-26).
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionCreate,
			EntityType: model.EntityProduct, EntityID: id, Label: name,
			After: map[string]any{"name": name, "price": price},
			At:    now,
		})
	})
	if err != nil {
		return model.ProductDetail{}, err
	}
	return s.detail(ctx, id)
}

// storeImage puts the picture where the screens can load it, and draws a
// placeholder tile when there is none, so a kiosk never shows an empty square.
func (s *Service) storeImage(img imageChoice, name string) (string, error) {
	if img.data != nil {
		return s.images.Save(img.data, img.ext)
	}
	return s.images.Save(imagex.Placeholder(name, utf8.RuneCountInString(name)), ".svg")
}

// Update edits a product.
//
// The change log gets only the fields that actually changed, and a save that
// changed nothing writes no row at all. Every machine holding the product gets
// a new catalog version, so no kiosk goes on selling at the old price.
func (s *Service) Update(ctx context.Context, actor model.Principal, id int64, in Input) (model.ProductDetail, error) {
	// The current picture is needed to tell "the admin sent the same picture
	// back" from "the admin uploaded a new one", which delivery has already
	// decided; this read is what makes the Keep case meaningful.
	current, err := s.products.Get(ctx, id)
	if errors.Is(err, model.ErrNoRecord) {
		return model.ProductDetail{}, errNotFound()
	}
	if err != nil {
		return model.ProductDetail{}, err
	}

	image := ""
	if current.ImageURL != nil {
		image = *current.ImageURL
	}

	fe := model.FieldErrors{}
	name, description, price := in.clean(fe)
	img := readImage(in.Image, image, fe)
	if err := fe.Err(); err != nil {
		return model.ProductDetail{}, err
	}

	if img.data != nil {
		// Again outside the unit of work: a file write cannot be rolled back.
		if image, err = s.images.Save(img.data, img.ext); err != nil {
			return model.ProductDetail{}, err
		}
	}

	err = s.uow.Do(ctx, func(tx uow.Tx) error {
		now := s.clock.Now()
		// Read under the lock, so what is logged as the "before" is what was
		// actually replaced and not a value that changed in between.
		was, err := tx.Products().LockForUpdate(ctx, id)
		if errors.Is(err, model.ErrNoRecord) {
			return errNotFound()
		}
		if err != nil {
			return err
		}

		before := map[string]any{
			"name": was.Name, "description": was.Description, "price": was.Price,
			"image": stringOf(was.ImageURL),
		}
		after := map[string]any{
			"name": name, "description": description, "price": price, "image": image,
		}
		b, a, changed := model.Changes(before, after)
		if !changed {
			return nil
		}
		if _, ok := a["image"]; ok {
			// A log row is for a person to read, and two URLs tell them
			// nothing; that the picture changed is the fact worth keeping.
			b["image"], a["image"] = "(gambar lama)", "(gambar baru)"
		}

		if err := tx.Products().Update(ctx, model.Product{
			ID: id, Name: name, Description: description, Price: price,
			ImageURL: &image, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := s.bumpMachinesSelling(ctx, tx, id); err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionUpdate,
			EntityType: model.EntityProduct, EntityID: id, Label: name,
			Before: b, After: a, At: now,
		})
	})
	if err != nil {
		return model.ProductDetail{}, err
	}
	return s.detail(ctx, id)
}

func stringOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Archive hides a product, and is refused while any slot still holds or plans
// it: the kiosk would be left offering something that is off the list (BR-10,
// TC-24). Archiving one already archived changes nothing.
func (s *Service) Archive(ctx context.Context, actor model.Principal, id int64) (model.ProductDetail, error) {
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		p, err := tx.Products().LockForUpdate(ctx, id)
		if errors.Is(err, model.ErrNoRecord) {
			return errNotFound()
		}
		if err != nil {
			return err
		}
		if !p.IsActive {
			return nil
		}

		used, err := tx.Products().Usage(ctx, id)
		if err != nil {
			return err
		}
		if slots := used[id]; len(slots) > 0 {
			// The refusal names the slots, because "it is in use" leaves the
			// admin hunting through every machine to find out where.
			labels := make([]string, len(slots))
			for i, u := range slots {
				labels[i] = u.MachineCode + " slot " + strconv.Itoa(u.SlotNumber)
			}
			return model.Conflict(model.CodeConflict,
				"Produk masih dipakai atau direncanakan di slot: "+strings.Join(labels, ", ")+".",
				map[string]any{"reason": "IN_USE", "slots": labels})
		}

		now := s.clock.Now()
		if err := tx.Products().SetActive(ctx, id, false, now); err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionArchive,
			EntityType: model.EntityProduct, EntityID: id, Label: p.Name,
			Before: map[string]any{"isActive": true},
			After:  map[string]any{"isActive": false},
			At:     now,
		})
	})
	if err != nil {
		return model.ProductDetail{}, err
	}
	return s.detail(ctx, id)
}

// Restore brings an archived product back to the lists. Restoring one that is
// already active changes nothing.
func (s *Service) Restore(ctx context.Context, actor model.Principal, id int64) (model.ProductDetail, error) {
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		p, err := tx.Products().LockForUpdate(ctx, id)
		if errors.Is(err, model.ErrNoRecord) {
			return errNotFound()
		}
		if err != nil {
			return err
		}
		if p.IsActive {
			return nil
		}
		now := s.clock.Now()
		if err := tx.Products().SetActive(ctx, id, true, now); err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionUpdate,
			EntityType: model.EntityProduct, EntityID: id, Label: p.Name,
			Before: map[string]any{"isActive": false},
			After:  map[string]any{"isActive": true},
			At:     now,
		})
	})
	if err != nil {
		return model.ProductDetail{}, err
	}
	return s.detail(ctx, id)
}

// bumpMachinesSelling raises the catalog version of every machine with this
// product loaded, so their kiosks reload rather than keep quoting the old price
// (BR-09).
//
// The slots are read without locks first and the machines locked after, because
// a purchase locks machine before slot: holding slot locks while waiting for a
// machine row is the one order that deadlocks.
func (s *Service) bumpMachinesSelling(ctx context.Context, tx uow.Tx, productID int64) error {
	ids, err := tx.Slots().MachineIDsHolding(ctx, productID)
	if err != nil {
		return err
	}
	return tx.Machines().BumpCatalogVersions(ctx, ids)
}
