package product_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	auditmock "vending/internal/repository/audit/mock"
	imagestoremock "vending/internal/repository/imagestore/mock"
	machinemock "vending/internal/repository/machine/mock"
	productmock "vending/internal/repository/product/mock"
	slotmock "vending/internal/repository/slot/mock"
	"vending/internal/repository/uow/uowtest"

	"vending/internal/model"
	"vending/internal/usecase/product"
)

// FR-26 says the change log can never miss a change. That is only true if the
// change and its log row commit together, and the only way to show it is to
// check that every write puts both through one unit of work — which is what
// most of these tests do, because nothing else can.

var (
	t0    = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	actor = model.Principal{UserID: 3, Name: "Admin", Username: "admin", Role: model.RoleAdmin}
)

type fixture struct {
	unit     *uowtest.Bundle
	products *productmock.MockRepository
	slots    *slotmock.MockRepository
	machines *machinemock.MockRepository
	audit    *auditmock.MockRepository
	images   *imagestoremock.MockRepository
	uc       *product.Service
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	f := &fixture{
		products: productmock.NewMockRepository(ctrl),
		slots:    slotmock.NewMockRepository(ctrl),
		machines: machinemock.NewMockRepository(ctrl),
		audit:    auditmock.NewMockRepository(ctrl),
		images:   imagestoremock.NewMockRepository(ctrl),
	}
	f.unit = &uowtest.Bundle{
		ProductRepo: f.products, SlotRepo: f.slots,
		MachineRepo: f.machines, AuditRepo: f.audit,
	}
	f.uc = product.New(f.unit, f.products, f.images, clock.New(func() time.Time { return t0 }), nil)
	return f, ctrl
}

func gacha() model.Product {
	url := "/uploads/abc.png"
	return model.Product{
		ID: 7, Name: "Gacha", Description: "kartu", Price: 25000, ImageURL: &url,
		IsActive: true, CreatedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(-time.Hour),
	}
}

func input() product.Input {
	return product.Input{Name: "Gacha", Description: "kartu", Price: 25000, PriceOK: true}
}

func code(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Code
}

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	e, ok := model.AsError(err)
	if !ok {
		t.Fatalf("err = %v, want a model.Error", err)
	}
	return e.Fields
}

// expectDetail is the read-back every write answers with.
func (f *fixture) expectDetail(p model.Product, used map[int64][]model.SlotUse) {
	f.products.EXPECT().Get(gomock.Any(), p.ID).Return(p, nil)
	f.products.EXPECT().Usage(gomock.Any(), p.ID).Return(used, nil)
}

// ---------------------------------------------------------------- create

// The product and its log row go through one unit of work, so there is no
// moment at which the product exists and the log does not (FR-26, TC-31).
func TestCreateWritesTheProductAndItsLogRowTogether(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.images.EXPECT().Save(gomock.Any(), ".svg").Return("/uploads/new.svg", nil)
	f.products.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, p model.Product) (int64, error) {
			if p.Name != "Gacha" || p.Price != 25000 || p.Description != "kartu" {
				t.Errorf("product = %+v", p)
			}
			if p.ImageURL == nil || *p.ImageURL != "/uploads/new.svg" {
				t.Errorf("imageURL = %v", p.ImageURL)
			}
			if !p.CreatedAt.Equal(t0) || !p.UpdatedAt.Equal(t0) {
				t.Errorf("times = %+v", p)
			}
			return 7, nil
		})
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionCreate || e.EntityType != model.EntityProduct {
				t.Errorf("entry = %+v", e)
			}
			if e.EntityID != 7 || e.Label != "Gacha" || e.ActorID != actor.UserID {
				t.Errorf("entry = %+v", e)
			}
			// Nothing was there before, so there is no before.
			if e.Before != nil {
				t.Errorf("before = %v, want nil for a create", e.Before)
			}
			if e.After["name"] != "Gacha" || e.After["price"] != int64(25000) {
				t.Errorf("after = %v", e.After)
			}
			if !e.At.Equal(t0) {
				t.Errorf("at = %v", e.At)
			}
			return nil
		})
	f.expectDetail(gacha(), nil)

	got, err := f.uc.Create(context.Background(), actor, input())
	if err != nil {
		t.Fatal(err)
	}
	if got.Product.ID != 7 {
		t.Errorf("detail = %+v", got)
	}
	// Never nil, so the admin web can iterate it without checking.
	if got.UsedIn == nil {
		t.Error("usedIn must be an empty list, not nil")
	}
	if f.unit.Attempts != 1 {
		t.Errorf("opened the unit of work %d times, want 1", f.unit.Attempts)
	}
}

// A product with no picture gets a placeholder tile drawn for it, so a kiosk
// never shows an empty square.
func TestCreateDrawsAPlaceholderWhenNoPictureWasSent(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.images.EXPECT().Save(gomock.Any(), ".svg").
		DoAndReturn(func(data []byte, _ string) (string, error) {
			if len(data) == 0 {
				t.Error("a placeholder must actually be drawn")
			}
			return "/uploads/new.svg", nil
		})
	f.products.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(7), nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	f.expectDetail(gacha(), nil)

	if _, err := f.uc.Create(context.Background(), actor, input()); err != nil {
		t.Fatal(err)
	}
}

// A real upload is stored as it came, under its own extension.
func TestCreateStoresAnUploadedPicture(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// A one-pixel PNG as the admin web sends it.
	url := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="
	in := input()
	in.Image = &url
	f.images.EXPECT().Save(gomock.Any(), ".png").Return("/uploads/new.png", nil)
	f.products.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(7), nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	f.expectDetail(gacha(), nil)

	if _, err := f.uc.Create(context.Background(), actor, in); err != nil {
		t.Fatal(err)
	}
}

// Every field at fault is reported at once, so a form marks all of them rather
// than making the admin save once per mistake. Nothing is stored or written.
func TestCreateReportsEveryBadFieldAtOnce(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	// No image is stored and no unit of work is opened.

	_, err := f.uc.Create(context.Background(), actor, product.Input{
		Name:        strings.Repeat("a", model.MaxProductNameRunes+1),
		Description: strings.Repeat("b", model.MaxProductDescriptionRunes+1),
		Price:       0, PriceOK: true,
	})
	if got := code(t, err); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
	got := fieldsOf(t, err)
	for _, field := range []string{"name", "description", "price"} {
		if got[field] == "" {
			t.Errorf("%s is not reported in %v", field, got)
		}
	}
	if f.unit.Attempts != 0 {
		t.Errorf("opened the unit of work %d times, want 0", f.unit.Attempts)
	}
}

func TestCreateRefusesABadPrice(t *testing.T) {
	cases := []struct {
		name  string
		price int64
		ok    bool
	}{
		{"not a number at all", 0, false},
		{"zero", 0, true},
		{"negative", -1, true},
		{"over the ceiling", model.MaxPrice + 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			in := input()
			in.Price, in.PriceOK = tc.price, tc.ok

			_, err := f.uc.Create(context.Background(), actor, in)
			if got := code(t, err); got != model.CodeValidation {
				t.Fatalf("code = %s, want %s", got, model.CodeValidation)
			}
			if fieldsOf(t, err)["price"] == "" {
				t.Error("the refusal must name the price")
			}
		})
	}
}

// Exactly the limits are allowed: they are the boundary, not past it.
func TestCreateAllowsTheExactLimits(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.images.EXPECT().Save(gomock.Any(), gomock.Any()).Return("/uploads/new.svg", nil)
	f.products.EXPECT().Create(gomock.Any(), gomock.Any()).Return(int64(7), nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	f.expectDetail(gacha(), nil)

	in := product.Input{
		Name:        strings.Repeat("é", model.MaxProductNameRunes), // characters, not bytes
		Description: strings.Repeat("b", model.MaxProductDescriptionRunes),
		Price:       model.MaxPrice, PriceOK: true,
	}
	if _, err := f.uc.Create(context.Background(), actor, in); err != nil {
		t.Fatal(err)
	}
}

// A picture that cannot be used is reported beside the other field errors, in
// one answer, rather than hiding them.
func TestCreateReportsAnUnusablePictureWithTheOtherFields(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	junk := "data:image/png;base64,bukan-gambar"
	in := input()
	in.Name, in.Image = "", &junk

	_, err := f.uc.Create(context.Background(), actor, in)
	got := fieldsOf(t, err)
	if got["image"] == "" || got["name"] == "" {
		t.Errorf("fields = %v, want both the picture and the name reported", got)
	}
}

// ---------------------------------------------------------------- update

// Only the fields that moved are logged, and the row says what the picture did
// rather than quoting two URLs at a person. Every machine selling the product
// is told to reload (BR-09, TC-10).
func TestUpdateLogsOnlyWhatChangedAndRefreshesTheKiosks(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	f.products.EXPECT().Get(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().Update(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, p model.Product) error {
			if p.ID != 7 || p.Price != 30000 || !p.UpdatedAt.Equal(t0) {
				t.Errorf("product = %+v", p)
			}
			return nil
		})
	f.slots.EXPECT().MachineIDsHolding(gomock.Any(), int64(7)).Return([]int64{9, 11}, nil)
	f.machines.EXPECT().BumpCatalogVersions(gomock.Any(), []int64{9, 11}).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionUpdate || e.EntityID != 7 {
				t.Errorf("entry = %+v", e)
			}
			// The name and description did not move, so they are not in the row.
			if len(e.After) != 1 || e.After["price"] != int64(30000) {
				t.Errorf("after = %v, want only the price", e.After)
			}
			if e.Before["price"] != int64(25000) {
				t.Errorf("before = %v", e.Before)
			}
			return nil
		})
	f.expectDetail(was, nil)

	in := input()
	in.Price = 30000
	in.Image = was.ImageURL // the admin web always sends the picture back
	if _, err := f.uc.Update(context.Background(), actor, 7, in); err != nil {
		t.Fatal(err)
	}
}

// A save that changed nothing writes nothing at all: no update, no log row, no
// catalog version. A change log full of "nothing happened" is one nobody reads.
func TestUpdateThatChangesNothingWritesNothing(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	f.products.EXPECT().Get(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	// No Update, no BumpCatalogVersions, no audit Write.
	f.expectDetail(was, nil)

	in := input()
	in.Image = was.ImageURL
	if _, err := f.uc.Update(context.Background(), actor, 7, in); err != nil {
		t.Fatal(err)
	}
}

// The same picture sent back is not stored again: the URL is cached for a year,
// so a second copy under a new name would be pure waste.
func TestUpdateDoesNotRestoreTheSamePicture(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	f.products.EXPECT().Get(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
	f.slots.EXPECT().MachineIDsHolding(gomock.Any(), int64(7)).Return(nil, nil)
	f.machines.EXPECT().BumpCatalogVersions(gomock.Any(), nil).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).Return(nil)
	f.expectDetail(was, nil)
	// images.Save is not expected at all.

	in := input()
	in.Name, in.Image = "Gacha Baru", was.ImageURL
	if _, err := f.uc.Update(context.Background(), actor, 7, in); err != nil {
		t.Fatal(err)
	}
}

// A new picture is stored and the row records that it changed, not which URLs
// were involved: two URLs tell a reader nothing.
func TestUpdateRecordsThatThePictureChangedWithoutQuotingURLs(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	url := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="
	f.products.EXPECT().Get(gomock.Any(), int64(7)).Return(was, nil)
	f.images.EXPECT().Save(gomock.Any(), ".png").Return("/uploads/new.png", nil)
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
	f.slots.EXPECT().MachineIDsHolding(gomock.Any(), int64(7)).Return(nil, nil)
	f.machines.EXPECT().BumpCatalogVersions(gomock.Any(), nil).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Before["image"] != "(gambar lama)" || e.After["image"] != "(gambar baru)" {
				t.Errorf("image change = %v -> %v", e.Before["image"], e.After["image"])
			}
			return nil
		})
	f.expectDetail(was, nil)

	in := input()
	in.Image = &url
	if _, err := f.uc.Update(context.Background(), actor, 7, in); err != nil {
		t.Fatal(err)
	}
}

// The "before" is read under the lock, so what is logged as replaced is what
// was actually replaced and not a value that moved in between.
func TestUpdateLogsTheValueItActuallyReplaced(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	stale := gacha()
	stale.Price = 25000
	locked := gacha()
	locked.Price = 27000 // somebody else got there first

	f.products.EXPECT().Get(gomock.Any(), int64(7)).Return(stale, nil)
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(locked, nil)
	f.products.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)
	f.slots.EXPECT().MachineIDsHolding(gomock.Any(), int64(7)).Return(nil, nil)
	f.machines.EXPECT().BumpCatalogVersions(gomock.Any(), nil).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Before["price"] != int64(27000) {
				t.Errorf("before price = %v, want the locked value 27000", e.Before["price"])
			}
			return nil
		})
	f.expectDetail(locked, nil)

	in := input()
	in.Price, in.Image = 30000, locked.ImageURL
	if _, err := f.uc.Update(context.Background(), actor, 7, in); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRefusesAProductThatIsNotThere(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.products.EXPECT().Get(gomock.Any(), int64(404)).Return(model.Product{}, model.ErrNoRecord)

	_, err := f.uc.Update(context.Background(), actor, 404, input())
	if got := code(t, err); got != model.CodeNotFound {
		t.Errorf("code = %s, want %s", got, model.CodeNotFound)
	}
}

// ---------------------------------------------------------------- archive

func TestArchiveHidesAProductNothingIsSelling(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().Usage(gomock.Any(), int64(7)).Return(nil, nil)
	f.products.EXPECT().SetActive(gomock.Any(), int64(7), false, t0).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Action != model.ActionArchive {
				t.Errorf("action = %s, want %s", e.Action, model.ActionArchive)
			}
			if e.Before["isActive"] != true || e.After["isActive"] != false {
				t.Errorf("entry = %v -> %v", e.Before, e.After)
			}
			return nil
		})
	archived := was
	archived.IsActive = false
	f.expectDetail(archived, nil)

	got, err := f.uc.Archive(context.Background(), actor, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got.Product.IsActive {
		t.Error("the product should be archived")
	}
}

// Archiving is refused while any slot holds or plans the product: the kiosk
// would be left offering something off the list (BR-10, TC-24). The refusal
// names the slots, because "it is in use" leaves the admin hunting.
func TestArchiveIsRefusedWhileSomethingStillSellsIt(t *testing.T) {
	cases := []struct {
		name string
		use  model.SlotUse
	}{
		{"loaded in a slot", model.SlotUse{MachineCode: "VM-01", SlotNumber: 3}},
		{"only planned for a slot", model.SlotUse{MachineCode: "VM-02", SlotNumber: 5, Planned: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(gacha(), nil)
			f.products.EXPECT().Usage(gomock.Any(), int64(7)).
				Return(map[int64][]model.SlotUse{7: {tc.use}}, nil)
			// Nothing is archived and no log row is written.

			_, err := f.uc.Archive(context.Background(), actor, 7)
			if got := code(t, err); got != model.CodeConflict {
				t.Fatalf("code = %s, want %s", got, model.CodeConflict)
			}
			e, _ := model.AsError(err)
			if e.Extra["reason"] != "IN_USE" {
				t.Errorf("extra = %v, want the reason IN_USE", e.Extra)
			}
			slots, ok := e.Extra["slots"].([]string)
			if !ok || len(slots) != 1 || !strings.Contains(slots[0], tc.use.MachineCode) {
				t.Errorf("slots = %v, want the slot named", e.Extra["slots"])
			}
			if !strings.Contains(e.Message, tc.use.MachineCode) {
				t.Errorf("message = %q, want the machine named", e.Message)
			}
		})
	}
}

// Archiving one already archived changes nothing, so a double click does not
// write a second log row saying the same thing.
func TestArchiveTwiceWritesNothingTheSecondTime(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	was.IsActive = false
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	// No Usage check, no SetActive, no audit Write.
	f.expectDetail(was, nil)

	if _, err := f.uc.Archive(context.Background(), actor, 7); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreBringsAnArchivedProductBack(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	was := gacha()
	was.IsActive = false
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(was, nil)
	f.products.EXPECT().SetActive(gomock.Any(), int64(7), true, t0).Return(nil)
	f.audit.EXPECT().Write(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, e model.AuditEntry) error {
			if e.Before["isActive"] != false || e.After["isActive"] != true {
				t.Errorf("entry = %v -> %v", e.Before, e.After)
			}
			return nil
		})
	f.expectDetail(gacha(), nil)

	if _, err := f.uc.Restore(context.Background(), actor, 7); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreTwiceWritesNothingTheSecondTime(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	f.products.EXPECT().LockForUpdate(gomock.Any(), int64(7)).Return(gacha(), nil)
	// No SetActive, no audit Write.
	f.expectDetail(gacha(), nil)

	if _, err := f.uc.Restore(context.Background(), actor, 7); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveAndRestoreRefuseAProductThatIsNotThere(t *testing.T) {
	for name, call := range map[string]func(*fixture) error{
		"archive": func(f *fixture) error {
			_, err := f.uc.Archive(context.Background(), actor, 404)
			return err
		},
		"restore": func(f *fixture) error {
			_, err := f.uc.Restore(context.Background(), actor, 404)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()
			f.products.EXPECT().LockForUpdate(gomock.Any(), int64(404)).
				Return(model.Product{}, model.ErrNoRecord)

			if got := code(t, call(f)); got != model.CodeNotFound {
				t.Errorf("code = %s, want %s", got, model.CodeNotFound)
			}
		})
	}
}

// ---------------------------------------------------------------- list

// The usage of every product is read in one query, not one per product: this is
// the screen most often opened, and it is the whole of it.
func TestListReadsAllUsageInOneQuery(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	other := gacha()
	other.ID = 8
	f.products.EXPECT().List(gomock.Any(), true).Return([]model.Product{gacha(), other}, nil)
	f.products.EXPECT().Usage(gomock.Any(), int64(0)).Return(map[int64][]model.SlotUse{
		7: {{MachineCode: "VM-01", SlotNumber: 1}},
	}, nil)

	got, err := f.uc.List(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want two products, got %d", len(got))
	}
	if len(got[0].UsedIn) != 1 || got[0].UsedIn[0].MachineCode != "VM-01" {
		t.Errorf("usedIn = %+v", got[0].UsedIn)
	}
	// A product used nowhere carries an empty list, not nil.
	if got[1].UsedIn == nil {
		t.Error("usedIn must be an empty list, not nil")
	}
}

// A storage failure is reported as itself, so an outage does not look to the
// admin like a form they filled in wrong.
func TestARepositoryFailureIsReported(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()
	boom := errors.New("the products table is unreachable")
	f.products.EXPECT().List(gomock.Any(), false).Return(nil, boom)

	if _, err := f.uc.List(context.Background(), false); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the repository's", err)
	}
}
