// Package restock owns an operator's visit to a machine (UC-04, UC-05).
//
// An operator signs in on the kiosk itself, sees every slot with its count and
// capacity, loads the machine, types in what is now in each slot and confirms.
// What that confirmation does, all of it together or none of it (TC-14 to
// TC-16, FR-15):
//
//   - records the visit, with a line per slot saying what it held and how many
//     were in it before and after — the evidence behind every count the system
//     reports;
//   - applies any product change an admin planned for a slot (FR-16);
//   - clears slots that a failed dispense had blocked, because a person has now
//     looked at them (BR-07);
//   - raises the machine's catalog version, so every kiosk reloads rather than
//     selling from the screen it had before the visit.
//
// Two things are deliberately strict here, because the operator is standing at
// the machine with the door open and cannot be asked twice:
//
// A count that cannot be right is refused and nothing at all is saved. Half a
// visit recorded is worse than none: it would leave the system confident about
// figures nobody checked.
//
// A visit sent twice is saved once. The kiosk generates the visit's id before
// it asks, so an answer lost on the way back can simply be asked for again,
// and the second ask returns the first answer (NFR-03, TC-13).
package restock

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"vending/internal/common/calendar"
	"vending/internal/common/clock"
	"vending/internal/common/paging"
	"vending/internal/common/security"
	"vending/internal/model"
	restockrepo "vending/internal/repository/restock"
	"vending/internal/repository/slot"
	"vending/internal/repository/uow"
)

//go:generate mockgen -source=restock.go -destination=mock/restock_mock.go -package=mock

// SaveInput is a confirmed visit. The numbers are already numbers: reading them
// off the wire is the delivery layer's work.
type SaveInput struct {
	// ID is the visit id the kiosk generated, which is what makes a resend safe.
	ID string
	// FinishedAt is when the operator confirmed, as the kiosk recorded it, or
	// nil when it did not say. An agent may file a visit it journaled while
	// offline, so its own time is kept when it is plausible.
	FinishedAt *time.Time
	Note       string
	Items      []ItemInput
}

// ItemInput is one slot as the kiosk sent it.
//
// The two "impossible" values are how a value this layer cannot use arrives
// without delivery having to decide what to say about it: an unreadable slot
// number comes in as 0 and an unreadable count as -1, and each then fails the
// same check as any other impossible value, with the same message.
type ItemInput struct {
	// Label is the slot number exactly as the client wrote it, so a refusal can
	// name the row the operator is looking at.
	Label string
	// SlotNumber is 0 when what arrived was not a number.
	SlotNumber int
	// QtyAfter is -1 when what arrived was not a whole number.
	QtyAfter int
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// Slots is what the restock screen shows: every slot of the machine with
	// its count, capacity and planned change (FR-14).
	Slots(ctx context.Context, m model.Machine) (model.MachineSlots, error)

	// Save records one confirmed visit (FR-15).
	Save(ctx context.Context, m model.Machine, p model.Principal, in SaveInput) (model.RestockSaved, error)

	// History is the admin's record of visits: who went where, when, and what
	// they left in each slot (UC-10, FR-25).
	History(ctx context.Context, q HistoryQuery) (model.RestockHistory, error)
}

// HistoryQuery is a request for the restock history. From and To are local
// calendar days (YYYY-MM-DD); empty means the last week. A zero MachineID or
// UserID means "don't filter on it".
type HistoryQuery struct {
	From, To       string
	MachineID      int64
	UserID         int64
	Page, PageSize int
	// All asks for every matching visit instead of one page, for the export.
	All bool
}

// The history's span and page: the last week by default, two years at most --
// the same ceiling every other admin screen has -- and ten visits to a page,
// because a visit is a block of slot lines and not a single row.
const (
	defaultHistoryDays = 7
	maxRangeDays       = 731
	historyPageSize    = 20
)

// Service is the implementation. It depends only on interfaces.
type Service struct {
	// uow is how a visit is saved: the session, its lines, every slot and the
	// catalog version have to commit together or not at all.
	uow uow.Repository
	// slots is bound to the pool, for showing the screen, which changes nothing.
	slots slot.Repository
	// history is bound to the pool too: the admin's record of past visits.
	history restockrepo.History
	clock   clock.Clock
	// loc is the zone the history's From and To days are read in.
	loc *time.Location
	log *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(u uow.Repository, slots slot.Repository, history restockrepo.History, c clock.Clock, loc *time.Location, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uow: u, slots: slots, history: history, clock: c, loc: loc, log: log}
}

// History is the admin's record of operator visits (UC-10, FR-25).
//
// What each visit changed is counted here rather than in the query: how many
// items a product change counts as is a rule about the evidence (model.Tally),
// and a rule in a SELECT is a rule nobody can test without a database.
func (s *Service) History(ctx context.Context, q HistoryQuery) (model.RestockHistory, error) {
	r, err := calendar.ParseRange(s.clock.Now(), s.loc, q.From, q.To, defaultHistoryDays, maxRangeDays)
	if err != nil {
		return model.RestockHistory{}, err
	}
	page := paging.Of(q.Page, q.PageSize, historyPageSize)
	limit, offset := page.Window(q.All)

	out, err := s.history.List(ctx, model.RestockFilter{
		Start: r.Start, End: r.End,
		MachineID: q.MachineID, UserID: q.UserID,
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return model.RestockHistory{}, err
	}
	for i := range out.Rows {
		out.Rows[i].ItemsAdded, out.Rows[i].SlotsChanged = model.Tally(out.Rows[i].Items)
	}
	out.Page, out.PageSize = page.Number, page.Size
	return out, nil
}

func (s *Service) Slots(ctx context.Context, m model.Machine) (model.MachineSlots, error) {
	slots, err := s.slots.ListByMachine(ctx, m.ID)
	if err != nil {
		return model.MachineSlots{}, err
	}
	return model.MachineSlots{Machine: m, Slots: slots}, nil
}

// refusal collects what is wrong with a visit, one message per slot.
//
// Every bad slot is reported, not just the first: the operator is at the machine
// and should be able to fix everything in one go rather than discovering the
// next problem after each correction. The order the slots are named in follows
// the order they were sent, so the message matches the screen.
type refusal struct {
	fields map[string]string
	labels []string
}

func (r *refusal) add(label, msg string) {
	if r.fields == nil {
		r.fields = map[string]string{}
	}
	key := "slot" + label
	if _, dup := r.fields[key]; dup {
		return
	}
	r.fields[key] = msg
	r.labels = append(r.labels, label)
}

func (r *refusal) err() error {
	if len(r.fields) == 0 {
		return nil
	}
	return model.InvalidInput("Jumlah slot "+strings.Join(r.labels, ", ")+" tidak valid.", r.fields)
}

// Save records one confirmed visit.
func (s *Service) Save(ctx context.Context, m model.Machine, p model.Principal, in SaveInput) (model.RestockSaved, error) {
	clientID, ok := security.NormalizeUUID(in.ID)
	if !ok {
		return model.RestockSaved{}, model.InvalidInput("ID kunjungan harus UUID.",
			map[string]string{"id": "UUID tidak valid"})
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Note)) > model.MaxNoteRunes {
		return model.RestockSaved{}, model.InvalidInput("Catatan paling panjang 255 karakter.",
			map[string]string{"note": "terlalu panjang"})
	}

	// Everything that can be judged without the machine is judged first, so an
	// obviously bad request never takes a lock.
	want, err := requested(in.Items)
	if err != nil {
		return model.RestockSaved{}, err
	}

	var saved model.RestockSaved
	err = s.uow.Do(ctx, func(tx uow.Tx) error {
		saved = model.RestockSaved{}

		// Lock order is machine, then slots, as in a purchase, so the two never
		// deadlock. Taking the machine exclusively also serializes two sends of
		// the same visit, which is what makes the check below conclusive.
		rec, err := tx.Machines().LockForUpdate(ctx, m.ID)
		if err != nil {
			return err
		}

		// A resend of a visit already saved returns the first result.
		switch existing, err := tx.Restocks().ByClientID(ctx, clientID); {
		case err == nil:
			if existing.MachineID != m.ID {
				return model.Conflict(model.CodeConflict, "ID kunjungan sudah dipakai mesin lain.", nil)
			}
			saved = model.RestockSaved{
				ClientID: clientID, SessionID: existing.ID,
				SavedAt: existing.FinishedAt, CatalogVersion: rec.CatalogVersion,
			}
			return nil
		case !errors.Is(err, model.ErrNoRecord):
			return err
		}

		slots, err := tx.Slots().LockAllByMachine(ctx, m.ID)
		if err != nil {
			return err
		}
		// Now the counts can be judged against the machine they are for.
		if err := checkAgainst(slots, want); err != nil {
			return err
		}

		now := s.clock.Now()
		session := model.RestockSession{
			ClientID: clientID, MachineID: m.ID, UserID: p.UserID,
			// The visit began when the operator signed in, so the record says
			// how long it took and not only when it was filed.
			StartedAt:  p.StartedAt,
			FinishedAt: finishedAt(in.FinishedAt, p.StartedAt, now),
			Note:       in.Note,
		}
		sessionID, err := tx.Restocks().Create(ctx, session)
		if errors.Is(err, model.ErrDuplicateID) {
			// Two sends raced past the read above. Rather than guess which won,
			// the kiosk is told to ask again, and the next ask finds the winner.
			return model.Conflict(model.CodeConflict, "Kunjungan ini sedang disimpan. Coba lagi.", nil)
		}
		if err != nil {
			return err
		}

		items := make([]model.RestockItem, 0, len(slots))
		for _, sl := range slots {
			after, qty, changed := apply(sl, want)
			items = append(items, model.RestockItem{
				SlotID: sl.ID, ProductID: after, QtyBefore: sl.Qty, QtyAfter: qty, ProductChanged: changed,
			})
			if err := tx.Slots().Restock(ctx, sl.ID, after, qty); err != nil {
				return err
			}
		}
		if err := tx.Restocks().AddItems(ctx, sessionID, items); err != nil {
			return err
		}
		// Every kiosk must reload: what it is showing was true before the door
		// was opened.
		if err := tx.Machines().BumpCatalogVersion(ctx, m.ID); err != nil {
			return err
		}

		saved = model.RestockSaved{
			ClientID: clientID, SessionID: sessionID,
			SavedAt: session.FinishedAt, CatalogVersion: rec.CatalogVersion + 1,
		}
		return nil
	})
	return saved, err
}

// requested turns the sent items into the counts wanted per slot number, and
// refuses what is impossible on its own terms: not a slot number at all, a slot
// sent twice, or a count that is not a whole number in range.
//
// What a particular slot can hold is not decided here, because it needs the
// machine.
func requested(items []ItemInput) (map[int]int, error) {
	want := map[int]int{}
	var bad refusal
	for _, it := range items {
		switch {
		case it.SlotNumber < 1 || it.SlotNumber > model.MaxSlots:
			bad.add(it.Label, "nomor slot tidak valid")
		case has(want, it.SlotNumber):
			// Two lines for one slot: there is no telling which the operator
			// meant, so neither is used.
			bad.add(it.Label, "slot dikirim dua kali")
		case it.QtyAfter < 0 || it.QtyAfter > model.MaxSlotQty:
			bad.add(it.Label, "jumlah harus bilangan bulat 0 sampai kapasitas")
		default:
			want[it.SlotNumber] = it.QtyAfter
		}
	}
	return want, bad.err()
}

func has(want map[int]int, n int) bool {
	_, ok := want[n]
	return ok
}

// checkAgainst judges the wanted counts against the machine's own slots: a
// count above the slot's capacity, a count above zero for a slot with no
// product, and a slot this machine does not have (TC-14 to TC-16).
func checkAgainst(slots []model.Slot, want map[int]int) error {
	var bad refusal
	known := map[int]bool{}
	for _, sl := range slots {
		known[sl.SlotNumber] = true
		q, sent := want[sl.SlotNumber]
		if !sent {
			continue
		}
		label := strconv.Itoa(sl.SlotNumber)
		switch {
		case q > sl.Capacity:
			bad.add(label, "maksimal "+strconv.Itoa(sl.Capacity))
		case q > 0 && heldAfter(sl) == nil:
			bad.add(label, "slot belum punya produk, isi harus 0")
		}
	}
	for n := range want {
		if !known[n] {
			bad.add(strconv.Itoa(n), "slot tidak ada di mesin ini")
		}
	}
	return bad.err()
}

// heldAfter is the product the slot will hold once this visit is applied: the
// planned change if there is one, otherwise what it holds now (FR-16).
func heldAfter(sl model.Slot) *model.Product {
	if sl.NextProduct != nil {
		return sl.NextProduct
	}
	return sl.Product
}

// apply works out what one slot becomes: the product it will hold, its count,
// and whether a planned product change was applied.
func apply(sl model.Slot, want map[int]int) (productID *int64, qty int, changed bool) {
	changed = sl.NextProduct != nil
	if p := heldAfter(sl); p != nil {
		id := p.ID
		productID = &id
	}
	qty, sent := want[sl.SlotNumber]
	if sent {
		return productID, qty, changed
	}
	// The operator did not type anything for this slot, so its count stands —
	// unless the product changed under it, in which case how many of the new
	// product are in there is not something anyone knows, and 0 is the only
	// honest answer. The next visit corrects it.
	if changed {
		return productID, 0, changed
	}
	return productID, sl.Qty, changed
}

// finishedAt decides when the visit ended. The kiosk's own time is kept when it
// is plausible — not before the visit began and not in the future — so a visit
// the agent journaled while offline keeps the time it actually happened rather
// than the time it was finally filed.
func finishedAt(sent *time.Time, startedAt, now time.Time) time.Time {
	if sent != nil && !sent.Before(startedAt) && !sent.After(now) {
		return *sent
	}
	return now
}
