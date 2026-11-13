# Rearchitecture Plan — vending-server → Clean Architecture

Target: restructure this repository to the conventions in
`vending-machine/docs/ARCHITECTURE.md` (the Kiosk Agent rulebook), so the server
and the agent share one layout, one port/adapter pattern and one testing story.

Module path stays `vending`. No behaviour change, no wire-contract change.

> **Status: done.** Phases 0 to 9 are complete; `internal/core`, `internal/httpapi`
> and `internal/seed` no longer exist, and the escape-hatch table in §7 is empty.
> This document is kept as the record of what the code looked like before, what
> was decided and why, and what the risks were. For the rules the code is held
> to **now**, read [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 0. Where the code is today

7,448 lines of Go in five internal packages:

| Package | Lines | What it is | Clean-architecture verdict |
|---|---|---|---|
| `internal/core` | 3,698 | Business rules **and** every SQL statement **and** every JSON wire shape **and** HTTP status codes **and** file writes | four layers fused into one |
| `internal/httpapi` | 2,056 (791 non-test) | Router, handlers, middleware, encode/decode | already **delivery**, just needs splitting |
| `internal/seed` | 553 | Demo data, writes raw SQL through `svc.DB()` | a repository + a usecase, bypassing the service |
| `internal/database` | 199 | Pool, `EnsureDatabase`, migrations | infrastructure |
| `internal/config`, `internal/security` | 220, 141 | Env parsing, argon2 + tokens | pure → `common` |
| `cmd/vending-server/main.go` | 418 | Composition root + 6 CLI subcommands | already the composition root |

The good news, and it decides the shape of this plan:

1. `internal/httpapi` is a genuinely thin delivery layer already. Handlers decode
   → call one service method → encode. Nothing in it needs rewriting, only moving.
2. `cmd/vending-server/main.go` already builds the graph in one place and injects
   it (`core.New(db, core.Options{...})`, `httpapi.New(svc, web, log, opts)`).
3. The whole test suite (`internal/httpapi/*_test.go`, 1,265 lines, 29 tests
   covering TC-01…TC-37) is **black-box over HTTP against real MySQL**. It never
   touches an internal symbol. That makes it a complete regression net for this
   refactor — it must stay green at every step and must not be rewritten until
   the move is finished.

The bad news, and it is the one real design problem:

4. Correctness here lives in **multi-table database transactions with an explicit
   lock order**. `CreateTransaction` locks `machines` (share) → `slots` (update) →
   counts open transactions → inserts. `SaveRestock` locks the machine, then every
   slot, then writes a session, its items, every slot and the catalog version.
   Every admin write writes its `audit_logs` row *in the same transaction*
   (FR-26's guarantee is "the change log can never miss a change"). A naive split
   into one repository per table, each holding `*sql.DB`, silently destroys
   oversell protection (TC-11, `TestConcurrentPurchasesNeverOversell`), the
   all-or-nothing restock (TC-14…TC-16) and the audit guarantee.

Section 2 is how to split the layers **without** losing that.

---

## 1. Target layout

```
cmd/vending-server/
├── main.go                  # flags, subcommand switch
├── wire.go                  # the one place concrete adapters are constructed
└── cli_*.go                 # migrate / create-admin / seed-demo / rotate-key / reconcile
internal/
├── model/                   # plain domain types + sentinel errors. No I/O, no internal imports.
│   ├── machine.go           #   Machine, MachineRef, Slot, SlotMap
│   ├── product.go           #   Product, ProductBrief
│   ├── transaction.go       #   Transaction, TransactionStatus, FailureReason consts
│   ├── restock.go           #   RestockSession, RestockItem
│   ├── user.go              #   User, Principal, Role, SessionKind
│   ├── audit.go             #   AuditEntry, Action, EntityType consts
│   ├── recap.go             #   Totals, SeriesPoint, BreakdownRow, DateRange
│   └── errors.go            #   model.Error{Code, Message, Fields, Extra} + constructors
├── common/
│   ├── config/              # ← internal/config
│   ├── logx/                # ← newLogger() out of main.go
│   ├── security/            # ← internal/security (argon2, tokens, UUID)
│   ├── clock/               # the injectable now(); today Service.clock
│   └── imagex/              # placeholderSVG + decodeImage (pure, no filesystem)
├── repository/
│   ├── mysqldb/             # ← internal/database: pool, EnsureDatabase, Migrate,
│   │                        #   the queryer interface, the deadlock-retry tx runner,
│   │                        #   null/scan helpers. Shared infra for every mysql adapter.
│   ├── uow/                 # port: uow.Repository — "run these calls atomically"
│   │   ├── uow.go
│   │   └── mysql/
│   ├── machine/             # port: machine.Repository   + mysql/
│   ├── slot/                # port: slot.Repository      + mysql/
│   ├── product/             # port: product.Repository   + mysql/
│   ├── transaction/         # port: transaction.Repository + mysql/
│   ├── restock/             # port: restock.Repository   + mysql/
│   ├── user/                # port: user.Repository      + mysql/
│   ├── session/             # port: session.Repository   + mysql/
│   ├── audit/               # port: audit.Repository     + mysql/
│   ├── report/              # port: report.Repository    + mysql/   (read-only aggregates)
│   ├── imagestore/          # port: imagestore.Repository + local/  (disk) — was core/images.go
│   └── seeddata/            # port: seeddata.Repository  + mysql/   (demo bulk insert/truncate)
├── usecase/
│   ├── auth/                # login, sessions, machine key, lockout        (UC-06, FR-13/23)
│   ├── catalog/             # GetCatalog, Heartbeat                        (UC-01)
│   ├── purchase/            # create, pay, result, cancel, report, status  (UC-02/03)
│   ├── restock/             # operator slots, save visit, list visits      (UC-04/05)
│   ├── product/             # product CRUD, archive/restore, image handling (UC-09)
│   ├── machine/             # machine CRUD, slot config, key rotation      (UC-10)
│   ├── operator/            # operator CRUD, password reset                (UC-11)
│   ├── recap/               # summary, recap, transaction history, period math (UC-07/08)
│   ├── audit/               # change-log read                              (UC-12)
│   ├── maintenance/         # ExpirePending, CleanupSessions, Reconcile, Ping
│   └── demo/                # seed / reset
└── delivery/
    ├── http/
    │   ├── router.go        # every route in one place
    │   ├── server.go        # Handler struct + New(usecase ports…)
    │   ├── auth.go          # the public/admin/machine/operator handler adapters
    │   ├── middleware.go    # ← httpapi/middleware.go, unchanged
    │   ├── request.go       # decode, bearer, pathID, query helpers
    │   ├── response.go      # writeJSON, writeError
    │   ├── status.go        # statusFor(code) — the code→HTTP-status table (see §3)
    │   ├── static.go        # admin web + /uploads
    │   ├── dto_*.go         # the JSON wire shapes + mappers, per story
    │   └── catalog.go purchase.go restock.go product.go machine.go
    │       operator.go recap.go audit.go
    └── scheduler/           # ← core/jobs.go RunJobs: the ticker that drives
                             #   usecase/maintenance. An inbound adapter, like http.
web/                         # unchanged (embedded admin web)
test/api/                    # ← internal/httpapi/*_test.go, moved verbatim:
                             #   full-stack black-box tests over real MySQL
docs/ARCHITECTURE.md         # this repo's own copy of the rulebook (deliverable of phase 9)
```

Deleted at the end: `internal/core`, `internal/httpapi`, `internal/seed`,
`internal/database`, `internal/config`, `internal/security`.

---

## 2. The one real design decision: transaction scope

The rulebook says a port's methods take **`model` types or std types, never driver
types** — no `*sql.Tx` in a signature. It also says a usecase orchestrates several
repositories. Here those two rules collide with the lock-ordered multi-table
transactions that make the purchase flow correct.

### 2.1 Chosen pattern: a unit-of-work port

Add one port whose whole job is "run these repository calls as one atomic unit".
It leaks no driver type: the callback receives a *bundle of ports*, not a `*sql.Tx`.

```go
// internal/repository/uow/uow.go
//
// Package uow runs a group of repository calls as one atomic unit. It exists
// because the purchase and restock rules are only correct when several
// repositories commit together, under one lock order (BR-06, BR-07, FR-26).
package uow

//go:generate mockgen -source=uow.go -destination=mock/uow_mock.go -package=mock

// Repository is the port the usecases depend on. Do commits when fn returns
// nil and rolls back otherwise; the adapter retries a fn that failed on a
// storage deadlock or lock timeout, so fn must keep no state between attempts.
type Repository interface {
	Do(ctx context.Context, fn func(Tx) error) error
	Read(ctx context.Context, fn func(Tx) error) error // one read-only snapshot
}

// Tx is the transaction-scoped bundle: every getter returns a repository bound
// to this unit of work, so everything it writes commits together.
type Tx interface {
	Machines() machine.Repository
	Slots() slot.Repository
	Products() product.Repository
	Transactions() transaction.Repository
	Restocks() restock.Repository
	Users() user.Repository
	Sessions() session.Repository
	Audit() audit.Repository
	Reports() report.Repository
}
```

A usecase then reads like the rules it implements, with no SQL in sight:

```go
// internal/usecase/purchase/create.go
func (s *Service) Create(ctx context.Context, m model.Machine, in CreateInput) (model.Transaction, error) {
	// …validation on in, no I/O…
	err := s.uow.Do(ctx, func(tx uow.Tx) error {
		// Lock order everywhere: machine, then slot. Never the other way (BR-06).
		mc, err := tx.Machines().LockShared(ctx, m.ID)
		if err != nil { return err }
		if !mc.IsActive { return model.ErrMachineInactive }
		if mc.CatalogVersion != in.CatalogVersion { stale = true; return nil }

		sl, err := tx.Slots().LockForUpdate(ctx, m.ID, in.SlotNumber)
		if err != nil { return err }
		if err := tx.Transactions().ExpireStale(ctx, sl.ID, s.paymentWindow); err != nil { return err }
		open, err := tx.Transactions().CountOpen(ctx, sl.ID)   // locking read
		if err != nil { return err }
		if sl.ProductID == 0 || sl.NeedsCheck || sl.Qty-open <= 0 { return model.ErrSoldOut }
		…
	})
}
```

Why this and not the alternatives:

| Option | Verdict |
|---|---|
| **UoW port with a `Tx` bundle** (chosen) | Keeps atomicity and lock order exactly as today; ports stay driver-free; a usecase test injects a fake `uow.Repository` whose `Do` calls `fn` with a bundle of mocks. |
| One repository per table, each with `*sql.DB` | Breaks TC-11 oversell protection and the audit guarantee. Non-starter. |
| Coarse "aggregate" repositories (`repository/purchase` owning transactions+slots+machines) | Fewer moving parts, but the SQL ends up carrying the business rules again — this is `internal/core` with a new name. |
| `*sql.Tx` in port signatures | Violates the rulebook and makes every port unmockable. |

### 2.2 Two bindings per adapter, one struct

Most reads need no transaction (`TransactionStatus`, `ListProducts`, the recap
queries). So every mysql adapter wraps a `queryer`, satisfied by both `*sql.DB`
and `*sql.Tx` — the interface `internal/core/products.go:14` already defines:

```go
// internal/repository/transaction/mysql/mysql.go
type MySQL struct{ q mysqldb.Queryer }

var _ transaction.Repository = (*MySQL)(nil)

func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }
```

`cmd` constructs the pool-bound instances and injects them for direct reads;
`uow/mysql` constructs tx-bound instances per `Do` call. Same type, same tests.

### 2.3 Locking is part of the port contract

Lock intent cannot stay implicit, or the next person writes `Get` where `LockForUpdate`
was required and oversell comes back. Rules for this codebase:

- Methods that take a row lock say so in the name: `LockShared`, `LockForUpdate`,
  `CountOpen` (documented as a locking read).
- Every port file documents the global lock order — **machine → slot → transaction** —
  and every usecase that locks more than one thing repeats it in a comment at the
  call site, as `core/purchase.go` and `core/restock.go` do today.
- `TestConcurrentPurchasesNeverOversell` and `TestTC11_LastItem` are the gate: they
  must pass with `-race -count=1` after every phase, or the phase is not done.

### 2.4 Where the retry lives

`Service.withTx` (`internal/core/service.go:101`) retries MySQL 1213/1205 up to
3 times. That is storage behaviour, not a business rule: it moves into
`uow/mysql`, and `mysqldb` keeps the error-number predicate. Usecases never see a
MySQL error number again — the duplicate-key case in `CreateTransaction`
(`purchase.go:273`) becomes a documented `transaction.Repository` behaviour
returning `model.ErrDuplicateID`, which the usecase handles by re-reading.

---

## 3. Errors: splitting the business failure from the HTTP status

`core.Error` (`internal/core/errors.go`) carries `Status int` — an HTTP concern
decided inside the business layer. The rulebook wants business errors as
`model` values, mapped to a status by delivery in one helper.

`model.Error` keeps `Code`, `Message` (Bahasa Indonesia, part of the contract),
`Fields` and `Extra`, and drops `Status`. `delivery/http/status.go` owns the map.
Derived from the current code, so the wire contract does not move:

| Code | Status | Code | Status |
|---|---|---|---|
| `BAD_REQUEST` | 400 | `VALIDATION` | 422 |
| `UNAUTHORIZED` | 401 | `INVALID_INPUT` | 422 |
| `INVALID_LOGIN` | 401 | `CONFLICT` | 409 |
| `INVALID_MACHINE_KEY` | 401 | `SOLD_OUT` | 409 |
| `FORBIDDEN` | 403 | `CATALOG_STALE` | 409 |
| `MACHINE_INACTIVE` | 403 | `TRANSACTION_CLOSED` | 409 |
| `NOT_FOUND` | 404 | `NOT_PAID` | 409 |
| `EXPIRED` | 410 | `NOT_REPORTABLE` | 409 |
| `PAYLOAD_TOO_LARGE` | 413 | `REPORT_WINDOW_CLOSED` | 409 |
| `LOCKED` | 423 | *(anything else / not a `model.Error`)* | 500 `INTERNAL` |

A table test asserting every code in `model` has an entry prevents a new code
silently becoming a 500.

---

## 4. The wire shapes (`core/views.go`)

295 lines of JSON-tagged structs that the admin web's `js/api.js` and the kiosk
both depend on by field name. The rulebook puts response DTOs in delivery, next
to the handler. Renaming a field here breaks two front-ends silently, so split in
two moves, not one:

1. **Mechanical**: move `views.go` whole into `delivery/http/dto.go`, no edits.
   Pure domain structs that have no JSON role (`Principal`, `Machine`, `DateRange`,
   `SlotReconciliation`) go to `model`. Build stays green, bytes on the wire
   identical.
2. **Per story**: split `dto.go` into `dto_catalog.go`, `dto_purchase.go`, … as each
   usecase slice lands, and add the `model → DTO` mapper next to it. Usecases return
   `model` types from then on.

Guard: phase 0 captures a golden JSON body for every endpoint; the diff must stay
empty through both moves.

---

## 5. File-by-file destination

| Today | Goes to |
|---|---|
| `cmd/vending-server/main.go` | `cmd/vending-server/{main,wire,cli_*}.go`; `newLogger` → `common/logx` |
| `internal/config/*` | `internal/common/config/` |
| `internal/security/*` | `internal/common/security/` |
| `internal/database/*` + `migrations/` | `internal/repository/mysqldb/` |
| `core/service.go` | `Options`/`New` → each usecase's `New`; `withTx`/`runTx` → `uow/mysql`; `ms`/`null*`/`placeholders`/`int64sToArgs` → `mysqldb`; `clock` → `common/clock`; `DB()`/`Now()`/`Location()`/`UploadDir()` **deleted** (escape hatches — see §7) |
| `core/errors.go` | `model/errors.go` + `delivery/http/status.go` + `mysqldb` (error numbers) |
| `core/views.go` | `delivery/http/dto_*.go` + `model/*` (see §4) |
| `core/auth.go` | `usecase/auth` + `repository/{user,session,machine}` |
| `core/purchase.go` | `usecase/catalog` (GetCatalog, Heartbeat) + `usecase/purchase` + `repository/{transaction,slot,machine,product}` |
| `core/products.go` | `usecase/product` + `repository/product` |
| `core/images.go` | `decodeImage`+`placeholderSVG` → `common/imagex` (pure); `storeImage` → `repository/imagestore/local` |
| `core/machines.go` | `usecase/machine` + `repository/{machine,slot}`; `intField`/`rawInt` → `delivery/http/request.go` (they parse `json.Number`, a wire concern) |
| `core/operators.go` | `usecase/operator` + `repository/user` |
| `core/restock.go` | `usecase/restock` + `repository/{restock,slot,machine}` |
| `core/transactions.go`, `core/recap.go` | `usecase/recap` + `repository/report` |
| `core/period.go` | `usecase/recap/period.go` (pure calendar math, single consumer) |
| `core/audit.go` | `writeAudit`/`ListAuditLogs` SQL → `repository/audit`; `changedFields` → `usecase/audit` |
| `core/reconcile.go` | `usecase/maintenance` + `repository/report` |
| `core/jobs.go` | rules → `usecase/maintenance`; the `RunJobs` ticker → `delivery/scheduler` |
| `core/period_test.go` | splits to `usecase/recap`, `usecase/audit`, `common/imagex` |
| `httpapi/server.go` | `delivery/http/{router,server,auth,static}.go` + `health` → `usecase/maintenance.Health` |
| `httpapi/admin.go` | `delivery/http/{auth,product,machine,operator,recap,audit}.go` |
| `httpapi/machine.go` | `delivery/http/{catalog,purchase,restock}.go` |
| `httpapi/middleware.go` | `delivery/http/middleware.go` (verbatim) |
| `httpapi/respond.go` | `delivery/http/{request,response,status}.go` |
| `httpapi/*_test.go` | `test/api/` verbatim (imports only); new thin handler tests with mock usecases are written in phase 9 |
| `internal/seed/demo.go` | `usecase/demo` (the scenario) + `repository/seeddata/mysql` (bulk insert, truncate) |
| `web/web.go` | unchanged |

---

## 6. Migration sequence

One slice at a time, **build and full test suite green between every step**, each
step its own commit. Order is chosen so the riskiest code moves last, onto
machinery already proven by the easier slices.

### Phase 0 — Safety net (do not skip)
The regression net only works if it actually runs: without `VENDING_TEST_DSN`
every integration test **skips silently** (`harness_test.go:66`).

- Stand up MySQL; confirm `make test-db VENDING_TEST_DSN='…'` runs all 29 tests green. Record the baseline.
- Add golden-JSON assertions: one captured response body per endpoint, compared byte-for-byte (42 routes). This is what makes §4 safe.
- `gofmt -l .` and `go vet ./...` clean; commit the baseline.
- Add `go.uber.org/mock` to `go.mod`; add a `make generate` target.

### Phase 1 — `internal/model`
Pure extraction of structs, constants and errors. Nothing imports it yet, so it
cannot break anything. `model.Error` without `Status`; `core.Error` becomes an
alias during the transition so `httpapi` keeps compiling.

### Phase 2 — `internal/common`
`config`, `security` → `common/`; `newLogger` → `common/logx`; the clock →
`common/clock`; `decodeImage`/`placeholderSVG` → `common/imagex`. Pure moves,
import rewrites only.

### Phase 3 — Repository infrastructure
`internal/database` → `repository/mysqldb` (plus `Queryer`, the tx runner with the
deadlock retry, the scan/null helpers). Define the `uow` port and its mysql
adapter, and the `imagestore` + `health` ports. `core.Service` keeps working,
now constructing itself over `mysqldb`. **No usecase moved yet.**

### Phase 4 — Slice 1: catalog (read-only, lowest risk)
First end-to-end slice, proving the pattern: `repository/{machine,slot,product}`
ports + mysql adapters → `usecase/catalog` → `delivery/http/catalog.go`.
`core.GetCatalog`/`Heartbeat` deleted. `cmd/wire.go` appears.
Gate: TC-01, TC-10, TC-33 green.

### Phase 5 — Slice 2: purchase (the hard one)
`repository/transaction` + `usecase/purchase`: create, pay, result, cancel,
report, status. This is where the lock order and the UoW pattern earn their keep.
Gate: TC-01/03/04/05/06/08/09/10/11/12/37 **and**
`TestConcurrentPurchasesNeverOversell` under `-race -count=5`.

### Phase 6 — Slice 3: auth + restock
`repository/{user,session}` + `usecase/auth` (lockout, idle timeouts, machine key);
`repository/restock` + `usecase/restock`. The delivery handler adapters
(`public`/`admin`/`machineOnly`/`operatorOnly`) move to `delivery/http/auth.go` and
now hold `auth.UseCase` instead of `*core.Service`.
Gate: TC-13…TC-21, TC-27.

### Phase 7 — Slice 4: admin
`repository/{product,audit,report}` + `usecase/{product,machine,operator,recap,audit}`.
The audit write becomes `tx.Audit().Write(ctx, model.AuditEntry{…})` inside the same
`uow.Do` as the change it records — the FR-26 guarantee, now explicit in the code.
Gate: TC-23…TC-26, TC-28…TC-31.

### Phase 8 — Jobs, demo seed, and the end of `core`
`usecase/maintenance` + `delivery/scheduler`; `repository/seeddata` +
`usecase/demo`, which removes `svc.DB()`. Delete `internal/core`,
`internal/httpapi`, `internal/seed`. `cmd/wire.go` is now the only file importing
an adapter.

### Phase 9 — Tests, guards and docs
- `//go:generate mockgen` on every port; `go generate ./...`; mocks in `mock/`.
- Per-layer unit tests the rulebook asks for: usecase with mock repositories
  (assert orchestration *and* lock order), delivery with mock usecases and
  `httptest` (assert status codes and JSON). Keep `test/api/` as the end-to-end net.
- **Import-direction guard**: a test over `go list -deps` that fails the build if
  `model` imports anything internal, `usecase` imports a `*/mysql` or `*/local`
  adapter or `net/http`, `delivery` imports `repository/*`, or `common` imports a
  layer. One cheap test that makes the rulebook self-enforcing.
- Write this repo's own `docs/ARCHITECTURE.md` (the rulebook, with the server's
  UoW section) and update `README.md`'s layout section.

---

## 7. Escape hatches that must close

These exist today and each one is a hole in the layering. Closing them is part of
the phase that touches them, not "later":

| Hole | Why it must go | Closed in |
|---|---|---|
| `Service.DB() *sql.DB` (`service.go:80`) | hands the raw pool to `seed`, which then writes SQL outside every rule | phase 8 |
| `Service.Now()`, `Location()`, `UploadDir()` | business state read by callers that should receive it injected | phases 2, 7, 8 |
| `core.Error.Status` | HTTP decided in the business layer | phase 1/§3 |
| `core.StoreImage`/`PlaceholderSVG` exported only for the seed | filesystem I/O in the business layer | phase 3 (`imagestore`) |
| `httpapi` holding `*core.Service` (a struct, not a port) | delivery cannot be tested without a database | phases 4–7 |
| `intField`/`rawInt` parsing `json.Number` inside `core` | wire decoding in the business layer | phase 7 |

---

## 8. Risks

| Risk | Mitigation |
|---|---|
| **Losing a lock or a lock order** → silent oversell, the worst failure here because it only shows under concurrency | §2.3: lock intent in port method names; `-race -count=5` concurrency gate every phase; review every `uow.Do` body against the original `core` function side by side |
| **Breaking the audit-in-same-transaction guarantee (FR-26)** | audit is a port inside `uow.Tx`, never a pool-bound call; TC-31 as the gate |
| **Wire-contract drift** when `views.go` splits | phase 0 golden JSON bodies; the two-step move in §4 |
| **A new error code silently becoming 500** | the `statusFor` table test in §3 |
| **The regression net skipping instead of running** | phase 0 makes the DSN a prerequisite; CI must fail if `VENDING_TEST_DSN` is unset |
| **`uow.Tx` growing into a god interface** | if it exceeds ~10 getters, switch to consumer-defined narrow bundles: each usecase declares the `Tx` subset it needs, and the one mysql tx struct satisfies all of them structurally. Plan for this, don't pre-empt it. |
| **Mid-refactor half-state** if work stops between phases | every phase ends green and shippable; `core` shrinks monotonically and never forks |

---

## 9. Effort

Rough, for one developer with the test suite green and MySQL available:

| Phase | Work |
|---|---|
| 0 safety net | 0.5–1 day (mostly the golden-JSON harness) |
| 1 model | 0.5 day |
| 2 common | 0.5 day |
| 3 repository infra + uow | 1–1.5 days |
| 4 catalog slice | 1 day (the pattern gets established here; budget for rework) |
| 5 purchase slice | 2–3 days (the locking) |
| 6 auth + restock | 2–3 days |
| 7 admin slices | 3–4 days (largest volume: products, machines, operators, recap) |
| 8 jobs, seed, delete `core` | 1–1.5 days |
| 9 mocks, per-layer tests, guards, docs | 2–3 days |
| | **~14–19 days** |

The sequence is designed to be interruptible: stopping after any phase leaves a
working, shippable server with less fused code than before.
