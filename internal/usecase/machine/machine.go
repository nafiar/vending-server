// Package machine owns the machines an admin registers and the slots in them
// (UC-08, FR-21, FR-22, FR-23).
//
// The machine key is the thread running through this package. A machine proves
// who it is with a key, and only the hash of that key is ever stored, so the
// server itself cannot tell anyone what it is. That has two consequences this
// code is shaped around: the key is shown exactly once, when it is created or
// rotated, and rotating it is the only recovery when it is lost — which is why
// rotation is an ordinary admin action rather than a database job.
//
// Slot planning is the other rule worth knowing before reading. An admin plans
// a product into a slot and nothing happens: the kiosk goes on showing and
// selling what is actually in the coil, and the plan is applied by the operator
// who next opens the machine (BR-08, TC-18). The server cannot put a product
// into a coil, so it must not pretend it has.
package machine

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"vending/internal/common/calendar"
	"vending/internal/common/clock"
	"vending/internal/common/security"
	"vending/internal/model"
	"vending/internal/repository/machine"
	"vending/internal/repository/report"
	"vending/internal/repository/slot"
	"vending/internal/repository/uow"
)

//go:generate mockgen -source=machine.go -destination=mock/machine_mock.go -package=mock

// codePattern is what a machine code may be: short, upper-case and safe to
// print on a sticker and read back over the phone.
var codePattern = regexp.MustCompile(`^[A-Z0-9-]{2,20}$`)

// keyPrefix makes a machine key recognisable in a log or a bug report without
// revealing it.
const keyPrefix = "vmk_"

// Input is a machine an admin is registering. The numbers are already numbers:
// deciding that what arrived is a number at all is the delivery layer's work,
// and an unreadable one arrives as 0, which fails the same range check.
type Input struct {
	Code      string
	Name      string
	Location  string
	SlotCount int
	Capacity  int
}

// SlotInput is a change to one slot (FR-22).
type SlotInput struct {
	Capacity int
	// NextProductID is the product planned for the slot, or nil to clear the
	// plan. A product id that could not be read arrives as 0, which fails the
	// same check as one that does not exist.
	NextProductID *int64
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// List returns every machine with the figures its card shows (UC-08).
	List(ctx context.Context) ([]model.MachineDetail, error)
	// Get returns one machine with its slots.
	Get(ctx context.Context, id int64) (model.MachineDetail, error)
	// Create registers a machine and its empty slots, and returns the key once
	// (FR-21, TC-26).
	Create(ctx context.Context, actor model.Principal, in Input) (model.MachineWithKey, error)
	// SetActive deactivates or reactivates a machine (BR-12, TC-27).
	SetActive(ctx context.Context, actor model.Principal, id int64, active bool) (model.MachineDetail, error)
	// RotateKey replaces a machine's key and returns the new one once (FR-23).
	RotateKey(ctx context.Context, actor model.Principal, id int64) (model.MachineWithKey, error)
	// RotateKeyByCode is RotateKey for the command line, where there is no
	// signed-in admin to attribute the change to.
	RotateKeyByCode(ctx context.Context, code string) (model.MachineWithKey, error)
	// UpdateSlot changes a slot's capacity and its planned product (FR-22).
	UpdateSlot(ctx context.Context, actor model.Principal, machineID int64, slotNumber int, in SlotInput) (model.Slot, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	uow uow.Repository
	// machines, slots and reports are pool-bound, for the reads that change
	// nothing.
	machines machine.Repository
	slots    slot.Repository
	reports  report.Repository
	clock    clock.Clock
	// loc is the zone whose calendar month the cards' revenue figure covers.
	loc *time.Location
	log *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(u uow.Repository, machines machine.Repository, slots slot.Repository, reports report.Repository, c clock.Clock, loc *time.Location, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uow: u, machines: machines, slots: slots, reports: reports, clock: c, loc: loc, log: log}
}

func errNotFound() *model.Error { return model.NotFound("Mesin tidak ditemukan.") }

// label is how a machine appears in the change log: the code to identify it and
// the name to recognise it, because a code alone means nothing a year later.
func label(m model.MachineRecord) string { return m.Code + " " + m.Name }

// List returns the machine cards.
func (s *Service) List(ctx context.Context) ([]model.MachineDetail, error) {
	return s.details(ctx, 0)
}

// Get returns one machine, with its slots filled in.
func (s *Service) Get(ctx context.Context, id int64) (model.MachineDetail, error) {
	out, err := s.details(ctx, id)
	if err != nil {
		return model.MachineDetail{}, err
	}
	if len(out) == 0 {
		return model.MachineDetail{}, errNotFound()
	}
	d := out[0]
	if d.Coils, err = s.slots.ListByMachine(ctx, id); err != nil {
		return model.MachineDetail{}, err
	}
	return d, nil
}

// details builds the cards: the records, then the three aggregates, each in one
// query for every machine asked about rather than one query per machine.
func (s *Service) details(ctx context.Context, onlyID int64) ([]model.MachineDetail, error) {
	records, err := s.machines.List(ctx, onlyID)
	if err != nil || len(records) == 0 {
		return nil, err
	}

	stats, err := s.reports.SlotStats(ctx, onlyID)
	if err != nil {
		return nil, err
	}
	// The revenue figure on a card is this calendar month in the server's zone,
	// which is why it is worked out here and not in the query.
	monthStart := calendar.StartOfMonth(s.clock.Now().In(s.loc)).UTC()
	sales, err := s.reports.MachineSales(ctx, monthStart, onlyID)
	if err != nil {
		return nil, err
	}
	lastSale, err := s.reports.MachineLastSale(ctx, onlyID)
	if err != nil {
		return nil, err
	}

	out := make([]model.MachineDetail, 0, len(records))
	for _, m := range records {
		d := model.MachineDetail{Machine: m, Slots: stats[m.ID], Month: sales[m.ID]}
		if at, ok := lastSale[m.ID]; ok {
			t := at
			d.LastSaleAt = &t
		}
		out = append(out, d)
	}
	return out, nil
}

// clean checks what an admin typed, adding a message per field at fault.
func (in Input) clean(fe model.FieldErrors) (code, name, location string) {
	code = strings.ToUpper(strings.TrimSpace(in.Code))
	name = strings.TrimSpace(in.Name)
	location = strings.TrimSpace(in.Location)

	if !codePattern.MatchString(code) {
		fe.Add("code", "Kode 2–20 karakter: huruf, angka atau tanda hubung.")
	}
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		fe.Add("name", "Nama wajib diisi.")
	case n > model.MaxMachineNameRunes:
		fe.Add("name", "Nama paling panjang 100 karakter.")
	}
	if utf8.RuneCountInString(location) > model.MaxMachineLocationRunes {
		fe.Add("location", "Lokasi paling panjang 150 karakter.")
	}
	if in.SlotCount < 1 || in.SlotCount > model.MaxSlots {
		fe.Add("slotCount", "Jumlah slot 1 sampai 60.")
	}
	if in.Capacity < 1 || in.Capacity > model.MaxSlotCapacity {
		fe.Add("capacity", "Kapasitas 1 sampai 30.")
	}
	return code, name, location
}

// Create registers a machine and its empty slots.
func (s *Service) Create(ctx context.Context, actor model.Principal, in Input) (model.MachineWithKey, error) {
	fe := model.FieldErrors{}
	code, name, location := in.clean(fe)
	if err := fe.Err(); err != nil {
		return model.MachineWithKey{}, err
	}

	// The key is made here and never stored, only its hash. This is the one
	// moment it exists anywhere outside the machine it is for.
	key := security.NewToken(keyPrefix)

	var id int64
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		id = 0
		now := s.clock.Now()
		rec := model.MachineRecord{Code: code, Name: name, Location: location, CreatedAt: now}

		var err error
		id, err = tx.Machines().Create(ctx, rec, security.HashToken(key))
		if errors.Is(err, model.ErrDuplicateID) {
			// Reported on the field the admin typed, not as a bare conflict, so
			// the form marks the box rather than showing a banner.
			return model.FieldErrors{"code": "Kode " + code + " sudah dipakai."}.Err()
		}
		if err != nil {
			return err
		}
		if err := tx.Slots().CreateMany(ctx, id, in.SlotCount, in.Capacity); err != nil {
			return err
		}

		after := map[string]any{
			"code": code, "name": name,
			"slots": int64(in.SlotCount), "capacity": int64(in.Capacity),
		}
		if location != "" {
			after["location"] = location
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionCreate,
			EntityType: model.EntityMachine, EntityID: id, Label: code + " " + name,
			After: after, At: now,
		})
	})
	if err != nil {
		return model.MachineWithKey{}, err
	}
	d, err := s.Get(ctx, id)
	return model.MachineWithKey{Machine: d, APIKey: key}, err
}

// SetActive deactivates or reactivates a machine.
//
// Deactivating ends the operator sessions on its kiosk in the same commit:
// whoever is signed in should not go on restocking a machine that is meant to
// be out of service. Setting it to what it already is changes nothing, so a
// double click writes no second log row.
func (s *Service) SetActive(ctx context.Context, actor model.Principal, id int64, active bool) (model.MachineDetail, error) {
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		m, err := tx.Machines().LockForUpdate(ctx, id)
		if errors.Is(err, model.ErrNoRecord) {
			return errNotFound()
		}
		if err != nil {
			return err
		}
		if m.IsActive == active {
			return nil
		}
		if err := tx.Machines().SetActive(ctx, id, active); err != nil {
			return err
		}
		if !active {
			if err := tx.Sessions().DeleteByMachine(ctx, id); err != nil {
				return err
			}
		}
		// DEACTIVATE is its own action in the log, because "a machine stopped
		// selling" is the thing somebody will come looking for.
		action := model.ActionUpdate
		if !active {
			action = model.ActionDeactivate
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: action,
			EntityType: model.EntityMachine, EntityID: id, Label: label(m),
			Before: map[string]any{"isActive": !active},
			After:  map[string]any{"isActive": active},
			At:     s.clock.Now(),
		})
	})
	if err != nil {
		return model.MachineDetail{}, err
	}
	return s.Get(ctx, id)
}

// RotateKey replaces a machine's key. The old one stops working the moment this
// commits, and the new one is shown once.
func (s *Service) RotateKey(ctx context.Context, actor model.Principal, id int64) (model.MachineWithKey, error) {
	key := security.NewToken(keyPrefix)
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		m, err := tx.Machines().LockForUpdate(ctx, id)
		if errors.Is(err, model.ErrNoRecord) {
			return errNotFound()
		}
		if err != nil {
			return err
		}
		if err := tx.Machines().SetAPIKeyHash(ctx, id, security.HashToken(key)); err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionUpdate,
			EntityType: model.EntityMachine, EntityID: id, Label: label(m),
			// The log says the key changed and not what it was: a change log is
			// read by people, and a key in it would be a key leaked.
			Before: map[string]any{"apiKey": "(kunci lama)"},
			After:  map[string]any{"apiKey": "(kunci baru)"},
			At:     s.clock.Now(),
		})
	})
	if err != nil {
		return model.MachineWithKey{}, err
	}
	d, err := s.Get(ctx, id)
	return model.MachineWithKey{Machine: d, APIKey: key}, err
}

// RotateKeyByCode is RotateKey for the command line, where a machine is named
// by its code and nobody is signed in.
//
// The change log needs a person, so the change is recorded under the first
// active admin. If there is none, the rotation is refused rather than logged
// against nobody: an unattributable change in the log is worse than no
// rotation, because the next person cannot tell what happened (FR-26).
func (s *Service) RotateKeyByCode(ctx context.Context, code string) (model.MachineWithKey, error) {
	m, err := s.machines.ByCode(ctx, strings.ToUpper(strings.TrimSpace(code)))
	if errors.Is(err, model.ErrNoRecord) {
		return model.MachineWithKey{}, model.NotFound("no machine with code " + code)
	}
	if err != nil {
		return model.MachineWithKey{}, err
	}
	admin, err := s.firstAdmin(ctx)
	if err != nil {
		return model.MachineWithKey{}, err
	}
	return s.RotateKey(ctx, model.Principal{UserID: admin}, m.ID)
}

// firstAdmin is who a command-line change is attributed to.
func (s *Service) firstAdmin(ctx context.Context) (int64, error) {
	var id int64
	err := s.uow.Read(ctx, func(tx uow.Tx) error {
		var err error
		id, err = tx.Users().FirstActiveAdminID(ctx)
		return err
	})
	if errors.Is(err, model.ErrNoRecord) {
		return 0, errors.New("create an admin account first (create-admin): the change log records who rotated the key")
	}
	return id, err
}

// UpdateSlot changes a slot's capacity and the product planned for it.
//
// Capacity may not go below what is in the coil right now: the count is a
// physical fact and the capacity is a claim about the coil, so the claim is the
// one that has to give. A plan for the product the coil already holds is not a
// change and is dropped.
func (s *Service) UpdateSlot(ctx context.Context, actor model.Principal, machineID int64, slotNumber int, in SlotInput) (model.Slot, error) {
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		// Only the slot row is locked. The machine and the product are read
		// without an exclusive lock, so this never waits in the opposite order
		// to a purchase, which takes machine and then slot.
		sl, err := tx.Slots().LockForUpdate(ctx, machineID, slotNumber)
		if errors.Is(err, model.ErrNoRecord) {
			return model.NotFound("Slot tidak ditemukan.")
		}
		if err != nil {
			return err
		}
		m, err := tx.Machines().Get(ctx, machineID)
		if err != nil {
			return err
		}

		fe := model.FieldErrors{}
		switch {
		case in.Capacity < 1 || in.Capacity > model.MaxSlotCapacity:
			fe.Add("capacity", "Kapasitas 1 sampai 30.")
		case in.Capacity < sl.Qty:
			fe.Add("capacity", "Tidak boleh di bawah isi saat ini ("+strconv.Itoa(sl.Qty)+").")
		}

		var nextID *int64
		nextName := ""
		if in.NextProductID != nil {
			// A shared lock: an archive of this product waits until the plan is
			// saved and then sees it, so a product can never be archived and
			// planned at the same moment (BR-10).
			p, err := tx.Products().LockShared(ctx, *in.NextProductID)
			switch {
			case errors.Is(err, model.ErrNoRecord), err == nil && !p.IsActive:
				fe.Add("nextProductId", "Pilih produk yang aktif.")
			case err != nil:
				return err
			case sl.Product != nil && sl.Product.ID == p.ID:
				// Planning what the coil already holds is not a change.
			default:
				id := p.ID
				nextID, nextName = &id, p.Name
			}
		}
		if err := fe.Err(); err != nil {
			return err
		}

		before := map[string]any{
			"capacity":    int64(sl.Capacity),
			"nextProduct": nameOrNil(sl.NextProduct),
		}
		after := map[string]any{
			"capacity":    int64(in.Capacity),
			"nextProduct": emptyAsNil(nextName),
		}
		b, a, changed := model.Changes(before, after)
		if !changed {
			return nil
		}

		if err := tx.Slots().SetPlan(ctx, sl.ID, in.Capacity, nextID); err != nil {
			return err
		}
		return tx.Audit().Write(ctx, model.AuditEntry{
			ActorID: actor.UserID, Action: model.ActionUpdate,
			EntityType: model.EntitySlot, EntityID: sl.ID,
			Label:  m.Code + " · Slot " + strconv.Itoa(slotNumber),
			Before: b, After: a, At: s.clock.Now(),
		})
	})
	if err != nil {
		return model.Slot{}, err
	}
	sl, err := s.slots.Get(ctx, machineID, slotNumber)
	if errors.Is(err, model.ErrNoRecord) {
		return model.Slot{}, model.NotFound("Slot tidak ditemukan.")
	}
	return sl, err
}

// nameOrNil is a planned product's name for the change log, or nil when there
// was no plan. nil rather than "" so that setting a plan reads as a change from
// nothing, not from an empty name.
func nameOrNil(p *model.Product) any {
	if p == nil {
		return nil
	}
	return p.Name
}

func emptyAsNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
