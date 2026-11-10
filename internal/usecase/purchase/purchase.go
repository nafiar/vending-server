// Package purchase owns one customer's purchase, from the tap on a slot to the
// item in the tray (UC-02, UC-03).
//
// The state machine (PRD, "Purchase sequence and transaction states"):
//
//	Create  ->  PENDING    the catalog version and availability are checked, the price copied
//	Pay         PENDING -> PAID        refused after the payment window: EXPIRED
//	Result      PAID    -> DISPENSED   the slot's count drops by one
//	                    -> FAILED      the count stands and the slot is blocked
//	Cancel      PENDING -> CANCELLED   the customer tapped "Batal"
//	                    -> EXPIRED     the kiosk's own timer ran out
//
// Two properties hold throughout, and most of the care in this file is about
// them:
//
// A final state never changes (BR-11). Every step therefore reads the
// transaction under a lock, looks at the status it is actually in, and either
// takes the one step allowed from there or reports the state as it stands.
//
// Every call is safe to repeat (NFR-03, TC-08). The kiosk generates the
// transaction id before it asks, so a request that timed out on the way back can
// simply be sent again: it finds the transaction already in the state it was
// asking for and answers with it, changing nothing.
package purchase

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"vending/internal/common/clock"
	"vending/internal/common/security"
	"vending/internal/model"
	"vending/internal/repository/transaction"
	"vending/internal/repository/uow"
	"vending/internal/usecase/catalog"
)

//go:generate mockgen -source=purchase.go -destination=mock/purchase_mock.go -package=mock

// reportWindow is how long after a sale "Produk tidak keluar?" is still
// accepted. The kiosk offers the button for 60 s; the rest covers a customer
// whose tap had to be retried (FR-09).
const reportWindow = 5 * time.Minute

// reasonPattern is the shape of a failure reason the agent reports: an
// upper-case identifier such as MOTOR_TIMEOUT. The set is open on purpose, so a
// new kind of hardware fault needs no server change to be recorded.
var reasonPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,49}$`)

// CreateInput is a request to start a purchase. The numbers are already numbers:
// turning what arrived on the wire into these is the delivery layer's work.
type CreateInput struct {
	// ID is the transaction id the kiosk generated, which is what makes the
	// request safe to repeat.
	ID string
	// SlotNumber is the slot the customer tapped.
	SlotNumber int
	// CatalogVersion is the version of the catalog the kiosk's screen was built
	// from. An old one is refused (FR-04, TC-10).
	CatalogVersion int64
}

// ResultInput is the agent's report of what the machine did.
type ResultInput struct {
	Status string // DISPENSED or FAILED
	// FailureReason is required for FAILED: NO_ACK, MOTOR_TIMEOUT, PORT_ERROR,
	// AGENT_RESTART or whatever else the agent learns to tell apart.
	FailureReason string
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// Create starts a purchase (FR-04).
	Create(ctx context.Context, m model.Machine, in CreateInput) (model.NewPurchase, error)
	// Status reports where one purchase stopped, for an agent that restarted
	// mid-purchase (BR-14).
	Status(ctx context.Context, m model.Machine, id string) (model.Transaction, error)
	// Pay confirms the simulated QRIS payment (UC-03).
	Pay(ctx context.Context, m model.Machine, id string) (model.Transaction, error)
	// Result records what the machine did with a paid purchase (FR-07, FR-08).
	Result(ctx context.Context, m model.Machine, id string, in ResultInput) (model.Transaction, error)
	// Cancel ends an unpaid purchase (FR-11).
	Cancel(ctx context.Context, m model.Machine, id, reason string) (model.Transaction, error)
	// Report records "Produk tidak keluar?" on a completed sale (FR-09).
	Report(ctx context.Context, m model.Machine, id string) (model.Transaction, error)
}

// Service is the implementation. It depends only on interfaces.
type Service struct {
	// uow is how every step that changes something runs: the status check and
	// the change it allows have to commit together, or a repeated request could
	// act twice.
	uow uow.Repository
	// transactions is bound to the pool, for the status read that changes
	// nothing and so needs no transaction of its own.
	transactions transaction.Repository
	// catalog supplies the fresh catalog a stale purchase is refused with, so
	// the kiosk can redraw its screen from the same answer that refused it.
	catalog       catalog.UseCase
	clock         clock.Clock
	paymentWindow time.Duration
	log           *slog.Logger
}

var _ UseCase = (*Service)(nil)

// New wires the dependencies. Called once in internal/app.
func New(u uow.Repository, transactions transaction.Repository, cat catalog.UseCase, c clock.Clock, paymentWindow time.Duration, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{uow: u, transactions: transactions, catalog: cat, clock: c, paymentWindow: paymentWindow, log: log}
}

// errNotFound is how every step answers for a transaction that is not there, or
// belongs to another machine. The two are deliberately indistinguishable: one
// machine's agent learns nothing about another's.
func errNotFound() *model.Error { return model.NotFound("Transaksi tidak ditemukan.") }

func errSoldOut() *model.Error { return model.Conflict(model.CodeSoldOut, "Produk habis.", nil) }

// Create starts a purchase.
//
// The kiosk sends the catalog version its screen was built from. An old one is
// refused together with the fresh catalog (CATALOG_STALE, TC-10), so a screen
// that has gone stale can never sell the wrong product or the wrong price. The
// price is copied into the transaction (BR-05, TC-09).
func (s *Service) Create(ctx context.Context, m model.Machine, in CreateInput) (model.NewPurchase, error) {
	id, ok := security.NormalizeUUID(in.ID)
	if !ok {
		return model.NewPurchase{}, model.InvalidInput("ID transaksi harus UUID.", map[string]string{"id": "UUID tidak valid"})
	}

	var out model.NewPurchase
	var stale bool
	attempt := func() error {
		return s.uow.Do(ctx, func(tx uow.Tx) error {
			// The unit of work may be retried, so everything this closure
			// reports is reset here rather than carried over from an attempt
			// that was rolled back.
			out, stale = model.NewPurchase{}, false

			// A repeated request finds the transaction it already created and
			// returns it, so the customer is not charged twice for one tap.
			switch existing, err := tx.Transactions().Get(ctx, id); {
			case err == nil:
				if existing.MachineID != m.ID {
					return errNotFound()
				}
				if existing.SlotNumber != in.SlotNumber {
					return model.Conflict(model.CodeConflict, "ID transaksi sudah dipakai untuk slot lain.", nil)
				}
				out = s.started(existing)
				return nil
			case !errors.Is(err, model.ErrNoRecord):
				return err
			}

			// Lock order everywhere: machine, then slot (BR-06).
			rec, err := tx.Machines().LockShared(ctx, m.ID)
			if err != nil {
				return err
			}
			if !rec.IsActive {
				return model.MachineInactive("Mesin ini dinonaktifkan di panel admin.")
			}
			if rec.CatalogVersion != in.CatalogVersion {
				stale = true
				return nil
			}

			sl, err := tx.Slots().LockForUpdate(ctx, m.ID, in.SlotNumber)
			if errors.Is(err, model.ErrNoRecord) {
				return model.NotFound("Slot tidak ditemukan.")
			}
			if err != nil {
				return err
			}

			now := s.clock.Now()
			// Abandoned payments on this slot are released first, so a customer
			// who walked away does not keep an item out of stock (BR-06).
			if err := tx.Transactions().ExpireStale(ctx, sl.ID, now.Add(-s.paymentWindow), s.paymentWindow); err != nil {
				return err
			}
			open, err := tx.Transactions().CountOpenShared(ctx, sl.ID)
			if err != nil {
				return err
			}
			// The same rule the kiosk's screen was drawn from (BR-04), decided
			// again here under the slot lock. That is what makes it binding.
			if !sl.Sellable(rec.IsActive, open) {
				return errSoldOut()
			}

			t := model.Transaction{
				ID: id, MachineID: m.ID, SlotID: sl.ID, SlotNumber: sl.SlotNumber,
				Status: model.StatusPending, Price: sl.Product.Price,
				CreatedAt: now, Product: *sl.Product,
			}
			if err := tx.Transactions().Create(ctx, t); err != nil {
				return err
			}
			out = model.NewPurchase{Transaction: t, ExpiresIn: s.paymentWindow}
			return nil
		})
	}

	err := attempt()
	if errors.Is(err, model.ErrDuplicateID) {
		// The same id was created concurrently, between the read above and the
		// insert. The second attempt takes the branch that returns it.
		err = attempt()
	}
	if err != nil {
		return model.NewPurchase{}, err
	}
	if stale {
		fresh, err := s.catalog.Get(ctx, m)
		if err != nil {
			return model.NewPurchase{}, err
		}
		// The catalog travels as a domain value; delivery maps it to the shape
		// the kiosk reads.
		return model.NewPurchase{}, model.Conflict(model.CodeCatalogStale, "Info produk diperbarui.",
			map[string]any{"catalog": fresh})
	}
	return out, nil
}

// started describes a purchase that already existed, with the time left on its
// payment window. Anything no longer PENDING has none, so a repeated request
// cannot restart the clock.
func (s *Service) started(t model.Transaction) model.NewPurchase {
	left := s.paymentWindow - s.clock.Now().Sub(t.CreatedAt)
	if left < 0 || t.Status != model.StatusPending {
		left = 0
	}
	return model.NewPurchase{Transaction: t, ExpiresIn: left}
}

// Status reports one purchase's state without changing anything, which is how an
// agent that restarted finds out where it stopped (BR-14).
func (s *Service) Status(ctx context.Context, m model.Machine, id string) (model.Transaction, error) {
	id, _ = security.NormalizeUUID(id)
	t, err := s.transactions.Get(ctx, id)
	if errors.Is(err, model.ErrNoRecord) || (err == nil && t.MachineID != m.ID) {
		return model.Transaction{}, errNotFound()
	}
	return t, err
}

// Pay confirms the simulated QRIS payment: PENDING -> PAID.
//
// After the payment window the purchase is refused and becomes EXPIRED (BR-06,
// TC-05). Paying one that is already PAID returns it unchanged, so an agent that
// restarted between the payment and the answer can simply ask again.
func (s *Service) Pay(ctx context.Context, m model.Machine, id string) (model.Transaction, error) {
	id, _ = security.NormalizeUUID(id)
	var out model.Transaction
	var expired bool
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		out, expired = model.Transaction{}, false
		t, err := s.locked(ctx, tx, m, id)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		switch t.Status {
		case model.StatusPending:
			if now.Sub(t.CreatedAt) > s.paymentWindow {
				// It ended when the window closed, not now: that is when the
				// customer lost the purchase.
				end := t.CreatedAt.Add(s.paymentWindow)
				expired = true
				return tx.Transactions().Finish(ctx, id, model.StatusExpired, end)
			}
			if err := tx.Transactions().MarkPaid(ctx, id, now); err != nil {
				return err
			}
			t.Status, t.PaidAt = model.StatusPaid, &now
		case model.StatusExpired:
			expired = true
			return nil
		case model.StatusCancelled:
			return model.Conflict(model.CodeTransactionClosed, "Transaksi sudah dibatalkan.", nil)
		}
		out = t
		return nil
	})
	if err != nil {
		return model.Transaction{}, err
	}
	if expired {
		return model.Transaction{}, model.Expired("Waktu pembayaran habis.")
	}
	return out, nil
}

// Result ends a paid purchase with what the machine actually did.
//
// DISPENSED takes the item off the slot's count in the same unit of work as the
// status change (FR-07), so the figure an operator sees and the sale it came
// from can never disagree. FAILED leaves the count alone and blocks the slot
// until the next visit (BR-07, TC-06): the item may be stuck half-way, and only
// a person can tell. Reporting the same result twice changes nothing (TC-08);
// reporting a different one is refused.
func (s *Service) Result(ctx context.Context, m model.Machine, id string, in ResultInput) (model.Transaction, error) {
	id, _ = security.NormalizeUUID(id)
	status := strings.ToUpper(strings.TrimSpace(in.Status))
	reason := strings.ToUpper(strings.TrimSpace(in.FailureReason))
	switch status {
	case model.StatusDispensed:
		reason = ""
	case model.StatusFailed:
		if !reasonPattern.MatchString(reason) {
			return model.Transaction{}, model.InvalidInput("failureReason wajib untuk FAILED, misalnya MOTOR_TIMEOUT.",
				map[string]string{"failureReason": "tidak valid"})
		}
	default:
		return model.Transaction{}, model.InvalidInput("status harus DISPENSED atau FAILED.",
			map[string]string{"status": "tidak valid"})
	}

	var out model.Transaction
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		out = model.Transaction{}
		t, err := s.locked(ctx, tx, m, id)
		if err != nil {
			return err
		}
		switch t.Status {
		case model.StatusPaid:
			// the one state a result can be recorded from
		case model.StatusDispensed, model.StatusFailed:
			if t.Status != status {
				return model.Conflict(model.CodeConflict, "Hasil transaksi ini sudah tercatat sebagai "+t.Status+".",
					map[string]any{"current": t})
			}
			out = t // a repeat of the same result: nothing changes
			return nil
		case model.StatusPending:
			return model.Conflict(model.CodeNotPaid, "Transaksi belum dibayar.", nil)
		default:
			return model.Conflict(model.CodeTransactionClosed, "Transaksi sudah "+strings.ToLower(t.Status)+".", nil)
		}

		now := s.clock.Now()
		if status == model.StatusDispensed {
			if err := tx.Transactions().Finish(ctx, id, model.StatusDispensed, now); err != nil {
				return err
			}
			took, err := tx.Slots().TakeOne(ctx, t.SlotID)
			if err != nil {
				return err
			}
			if !took {
				// The item left the machine but the server's count was already
				// zero. The sale stands; the count is for the next visit to fix.
				s.log.Warn("dispensed from a slot whose count is already 0; check it at the next visit",
					"transaction", id, "slot_id", t.SlotID)
			}
		} else {
			if err := tx.Transactions().Fail(ctx, id, reason, now); err != nil {
				return err
			}
			// Machine before slot, the same lock order as a purchase: the
			// catalog version rises so every kiosk reloads and stops offering
			// the slot that is now blocked.
			if err := tx.Machines().BumpCatalogVersion(ctx, m.ID); err != nil {
				return err
			}
			if err := tx.Slots().MarkNeedsCheck(ctx, t.SlotID); err != nil {
				return err
			}
			t.FailureReason = &reason
		}
		t.Status, t.FinishedAt = status, &now
		out = t
		return nil
	})
	return out, err
}

// Cancel ends an unpaid purchase. "Batal" gives CANCELLED and the kiosk's own
// timer gives EXPIRED (FR-11), which is how a recap tells a customer who changed
// their mind from one who walked away. Anything already final is returned as it
// stands.
func (s *Service) Cancel(ctx context.Context, m model.Machine, id, reason string) (model.Transaction, error) {
	id, _ = security.NormalizeUUID(id)
	next := model.StatusCancelled
	if strings.EqualFold(reason, "TIMEOUT") {
		next = model.StatusExpired
	}
	var out model.Transaction
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		out = model.Transaction{}
		t, err := s.locked(ctx, tx, m, id)
		if err != nil {
			return err
		}
		if t.Status == model.StatusPending {
			now := s.clock.Now()
			if err := tx.Transactions().Finish(ctx, id, next, now); err != nil {
				return err
			}
			t.Status, t.FinishedAt = next, &now
		}
		out = t
		return nil
	})
	return out, err
}

// Report records the customer's "Produk tidak keluar?" on a sale the machine
// says it completed (FR-09, TC-12).
//
// The sale keeps its status and still counts as revenue; it is flagged for an
// admin to look at (BR-03). Reporting twice is the first report's answer again.
func (s *Service) Report(ctx context.Context, m model.Machine, id string) (model.Transaction, error) {
	id, _ = security.NormalizeUUID(id)
	var out model.Transaction
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		out = model.Transaction{}
		t, err := s.locked(ctx, tx, m, id)
		if err != nil {
			return err
		}
		if t.Status != model.StatusDispensed {
			return model.Conflict(model.CodeNotReportable, "Hanya penjualan yang berhasil yang bisa dilaporkan.", nil)
		}
		if t.CustomerReportedAt != nil {
			out = t
			return nil
		}
		now := s.clock.Now()
		// Long after the fact, nobody can tell what happened at the machine, so
		// the window closes rather than collecting reports that cannot be acted
		// on.
		if t.FinishedAt != nil && now.Sub(*t.FinishedAt) > reportWindow {
			return model.Conflict(model.CodeReportWindowShut, "Laporan hanya bisa dikirim sesaat setelah pembelian.", nil)
		}
		if err := tx.Transactions().MarkReported(ctx, id, now); err != nil {
			return err
		}
		t.CustomerReportedAt = &now
		out = t
		return nil
	})
	return out, err
}

// locked reads a transaction for a step that is about to change it: under the
// row's exclusive lock, and only if it belongs to the machine that asked.
func (s *Service) locked(ctx context.Context, tx uow.Tx, m model.Machine, id string) (model.Transaction, error) {
	t, err := tx.Transactions().LockForUpdate(ctx, id)
	if errors.Is(err, model.ErrNoRecord) || (err == nil && t.MachineID != m.ID) {
		return model.Transaction{}, errNotFound()
	}
	return t, err
}
