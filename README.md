# Vending server (Go + MySQL)

The server of the vending machine information system in <https://claude.ai/artifact/8AmhGkq8GZBeBVNPALQyVe#08b44a7f-9024>. One binary:

- serves the **admin web** (the same HTML/CSS/JS pages, now on the real API) at `/`;
- provides the **admin API** the admin web calls, at `/api/admin/...`;
- provides the **machine API** the Kiosk Agent on each machine calls, at `/api/machine/...`;
- stores everything in **MySQL 8.0+ or MariaDB 10.6+**, and applies its own schema on start-up.

To put it on the GCP VM with HTTPS, see **[DEPLOY.md](DEPLOY.md)**: one command from your laptop.

Only standard-library HTTP and `database/sql`, plus two dependencies: the MySQL driver and `golang.org/x/crypto` (Argon2id).

## Run it on your computer

You need **Go 1.24 or newer** and a running **MySQL 8.0+ or MariaDB 10.6+**.

1. **Point it at MySQL.** Copy the example settings and fill in your MySQL user:

   ```bash
   cp .env.example .env            # Windows PowerShell: Copy-Item .env.example .env
   ```

   Edit `DB_USER` and `DB_PASSWORD` in `.env`. The server creates the `vending` database and its tables on first start, if that user may create databases. Otherwise create an empty database yourself and put its name in `DB_NAME`.

2. **Load demo data (optional).** Three machines with about 90 days of sales and restock visits:

   ```bash
   go run ./cmd/vending-server seed-demo
   ```

   It prints the demo logins (`admin / admin123`, operators `budi / budi123` and `siti / siti123`) and the three machine keys. For an empty system, create your own admin instead:

   ```bash
   go run ./cmd/vending-server create-admin -username admin -name "Nama Anda"
   ```

   Without `-password` it generates one and prints it once.

3. **Start the server:**

   ```bash
   go run ./cmd/vending-server
   ```

   Open <http://localhost:8080> and sign in.

To get a binary instead: `go build -o bin/vending-server ./cmd/vending-server` (on Windows: `go build -o bin\vending-server.exe ./cmd/vending-server`). The admin web is embedded, so the binary and a `.env` file are all a machine needs.

## Commands

| Command | What it does |
| --- | --- |
| `vending-server` or `vending-server serve` | Start the server. `-addr :9000` overrides `HTTP_ADDR` |
| `vending-server migrate` | Create the database if needed and apply pending migrations, then exit |
| `vending-server create-admin` | Add an admin: `-username`, `-name`, `-password` (optional). `-reset` sets a new password on an existing account |
| `vending-server seed-demo` | Load demo data into an empty database. `-reset` deletes every row first |
| `vending-server reset` | Delete every row and load the demo data again. The machine keys of VM-01 to VM-03 stay the same, so a configured kiosk keeps working |
| `vending-server rotate-key -machine VM-01` | Print a new machine key; the old one stops working |
| `vending-server reconcile` | Check every slot: count at the last visit, minus DISPENSED sales since, must equal the count now (NFR-05) |

## Settings

Read from environment variables, or from `.env` in the working directory. A real environment variable wins over the file.

| Variable | Default | Meaning |
| --- | --- | --- |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME` | `127.0.0.1`, `3306`, `root`, empty, `vending` | MySQL connection |
| `DB_DSN` | | One Go MySQL DSN instead of the five above, e.g. `user:pass@tcp(host:3306)/vending` |
| `HTTP_ADDR` | `:8080` | Listen address |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | | Serve HTTPS directly (NFR-04); leave empty on localhost or behind a TLS proxy |
| `TIMEZONE` | `Asia/Jakarta` | What "today", a day and an hour mean in the recap. Use a zone without daylight saving time |
| `UPLOAD_DIR` | `data/uploads` | Where product images are written; served at `/uploads/` |
| `AUTO_MIGRATE` | `true` | Create the database and apply migrations on start-up |
| `ADMIN_SESSION_IDLE` | `30m` | Admin web idle logout (BR-13) |
| `OPERATOR_SESSION_IDLE` | `2m` | Operator mode idle logout (BR-13) |
| `PAYMENT_WINDOW` | `60s` | How long a PENDING transaction can be paid (BR-06) |
| `LOG_FORMAT`, `LOG_LEVEL` | `text`, `info` | `json` for log collectors; `debug` also logs static files |
| `DEMO_MODE` | `false` | `true` shows **Reset data demo** in the admin web (`POST /api/admin/demo/reset`). For demo servers only |

## Try the machine API with curl

After `seed-demo`, use the VM-01 key it printed. This is the same sequence the Kiosk Agent will run for one purchase:

```bash
KEY='vmk_...'                          # VM-01's key from seed-demo
API=http://localhost:8080/api/machine

curl -s $API/catalog -H "X-Machine-Key: $KEY"                     # note catalogVersion
ID=$(python3 -c 'import uuid; print(uuid.uuid4())')                # the kiosk generates the id
curl -s -X POST $API/transactions -H "X-Machine-Key: $KEY" \
     -d "{\"id\":\"$ID\",\"slotNumber\":2,\"catalogVersion\":VERSION}"   # -> PENDING
curl -s -X POST $API/transactions/$ID/pay -H "X-Machine-Key: $KEY"       # -> PAID
curl -s -X POST $API/transactions/$ID/result -H "X-Machine-Key: $KEY" \
     -d '{"status":"DISPENSED"}'                                          # -> DISPENSED, count - 1
```

The sale appears in the admin web's recap and transaction history at once.

## API

Every body is JSON. Every time is a Unix timestamp in milliseconds (UTC); dates in queries are `YYYY-MM-DD` in WIB. An error is a non-2xx status with

```json
{ "code": "VALIDATION", "message": "Periksa isian yang ditandai.", "fields": { "price": "Harga harus bilangan bulat lebih dari 0." } }
```

where `fields` appears on validation errors and some codes add data (`attemptsLeft`, `retryInSeconds`, `catalog`, `slots`). Messages are in Bahasa Indonesia because the screens show them as they are.

### Admin API: `/api/admin`, with `Authorization: Bearer <admin token>`

The request and response shapes are the contract in the admin web's README, which `web/admin/js/api.js` already calls.

| Method and path | What it does |
| --- | --- |
| `POST /login` · `POST /logout` · `GET /me` | Sign in (`INVALID_LOGIN` 401, `LOCKED` 423, `FORBIDDEN` 403 for operator accounts), sign out, who am I |
| `GET /recap/summary?machineId=` | Today, the same time yesterday, the last 7 days, this month |
| `GET /recap?from=&to=&machineId=&groupBy=day\|week\|month&breakdown=product\|slot` | Totals, chart series, per machine, top 5 products, breakdown |
| `GET /transactions?from=&to=&machineId=&slotNumber=&status=&flag=problem\|reported&page=&pageSize=&all=1` | Transaction history with the DISPENSED total of the filtered set |
| `GET /restock-sessions?from=&to=&machineId=&userId=&page=&pageSize=` | Visits with their lines per slot |
| `GET /products?includeArchived=1` · `POST /products` · `PUT /products/{id}` | Products; `imageUrl` takes a JPG/PNG data URL up to 2 MB |
| `POST /products/{id}/archive` · `/restore` | Archive (`CONFLICT` 409 with `slots` while a slot holds or plans it), restore |
| `GET /machines` · `GET /machines/{id}` · `POST /machines` | Machines with slot and revenue figures; registering returns the key once |
| `POST /machines/{id}/activate` · `/deactivate` · `/rotate-key` | A deactivated machine's key is refused; rotating returns a new key once |
| `PUT /machines/{id}/slots/{n}` | Capacity (1–30, not below the count) and planned next product |
| `GET /operators` · `POST /operators` | Staff accounts with visit figures |
| `POST /operators/{id}/activate` · `/deactivate` · `/reset-password` | Reset returns a one-time `temporaryPassword` |
| `GET /audit-logs?from=&to=&entityType=` | The change log |
| `GET /reconcile` | The same check as the `reconcile` command |
| `POST /demo/reset` | Only with `DEMO_MODE=true`: delete every row and load the demo data again |

### Machine API: `/api/machine`, with `X-Machine-Key: <machine key>`

| Method and path | State change | Errors |
| --- | --- | --- |
| `GET /catalog` | none: slots, products, availability (BR-04), `catalogVersion` | |
| `POST /heartbeat` | none: updates last contact (FR-28), returns `catalogVersion` | |
| `POST /transactions` `{id, slotNumber, catalogVersion}` | → PENDING, price copied | `CATALOG_STALE` 409 with `catalog`, `SOLD_OUT` 409 |
| `GET /transactions/{id}` | none: the current state, for an agent that restarted | `NOT_FOUND` 404 |
| `POST /transactions/{id}/pay` | PENDING → PAID | `EXPIRED` 410 after 60 s |
| `POST /transactions/{id}/result` `{status, failureReason}` | PAID → DISPENSED (count − 1) or FAILED (slot blocked) | `NOT_PAID` 409, `CONFLICT` 409 for a different result |
| `POST /transactions/{id}/cancel` `{reason: CANCEL\|TIMEOUT}` | PENDING → CANCELLED or EXPIRED | |
| `POST /transactions/{id}/report` | none: sets `customer_reported_at` on a DISPENSED sale | `NOT_REPORTABLE`, `REPORT_WINDOW_CLOSED` |
| `POST /operator/login` `{username, password}` · `POST /operator/logout` | operator session bound to this machine | `INVALID_LOGIN` 401 with `attemptsLeft`, `LOCKED` 423 |
| `GET /operator/slots` (+ `Authorization: Bearer <operator token>`) | none: the restock screen | `UNAUTHORIZED` 401 |
| `POST /restock-sessions` `{id, finishedAt?, note?, items: [{slotNumber, qtyAfter}]}` (+ operator token) | one visit: counts, planned products applied, blocks cleared, version + 1 | `INVALID_INPUT` 422 with a field per slot |

A wrong machine key gives `INVALID_MACHINE_KEY` 401; a deactivated machine gives `MACHINE_INACTIVE` 403. Every call is safe to repeat: the kiosk generates the transaction and visit ids, and a repeated call returns the current state without changing anything (NFR-03).

## How the rules are enforced

- **Revenue** is the sum of `price` over DISPENSED transactions (BR-03), and `price` is copied when the transaction is created (BR-05), so a price change never rewrites history. The count drops only on DISPENSED (BR-07).
- **The last item can't be sold twice.** Creating a transaction locks the machine row (shared) and then the slot row, and counts open transactions with a locking read, so concurrent requests queue on the slot. A test fires 12 purchases at a slot holding 3: exactly 3 succeed.
- **A stale screen can't sell.** Every change to what a machine shows raises its `catalog_version` (BR-09), and a purchase must send the version its screen was built from.
- **Lock order** is always machine, then slot, in purchases, results and restock visits; product and slot edits read other rows without locks. InnoDB deadlocks or lock timeouts that still happen are retried.
- **The change log is written in the same database transaction** as the change it records, so it can't miss one.
- **Passwords** are Argon2id hashes (OWASP's first choice, NFR-04). Session tokens and machine keys are 256-bit random values stored as SHA-256 hashes: a slow hash is for low-entropy passwords, and a fast one lets the server find a key by index.
- **Logins** lock for 5 minutes after 5 wrong passwords, even against the right one (FR-13). Admin sessions end after 30 minutes idle, operator sessions after 2 (BR-13). Unknown usernames take as long as known ones.
- **Times** are stored in UTC; "today", days, weeks (Monday start) and months are computed in WIB.
- **Images** are checked by content, not name: JPG or PNG, at most 2 MB and 6000 × 6000 px, saved under a random name. A product without an image gets a placeholder tile. Uploads are served with a sandboxing CSP header.
- **Abandoned payments** expire every minute (BR-06), and at start-up.

## Tests

```bash
go test ./...                                                        # unit tests, no database
VENDING_TEST_DSN='root:secret@tcp(127.0.0.1:3306)/' go test -race ./... # plus the MySQL integration tests
```

The integration tests run the real HTTP handlers against MySQL. Each test creates its own database, runs on a fake clock and drops the database afterwards. They map to the PRD's test cases:

| PRD test | Go test |
| --- | --- |
| TC-01 | `TestTC01_Purchase` |
| TC-03, TC-04 | `TestTC03_TC04_CancelAndTimeout` |
| TC-05 | `TestTC05_PayAfterWindow` |
| TC-06 | `TestTC06_FailedDispense` |
| TC-08, NFR-03 | `TestTC08_RepeatsChangeNothing` |
| TC-09 | `TestTC09_PriceSnapshot` |
| TC-10 | `TestTC10_StaleCatalog` |
| TC-11, BR-04 | `TestTC11_LastItem`, `TestConcurrentPurchasesNeverOversell` |
| TC-12 | `TestTC12_CustomerReport` |
| TC-13 | `TestTC13_RestockVisit` |
| TC-14 to TC-16 | `TestTC14_TC16_RestockBoundaries` |
| TC-17, TC-18 | `TestTC17_TC18_PlannedChange` |
| TC-19 | `TestTC19_Lockout` |
| TC-20 | `TestTC20_OperatorIdle` |
| TC-21 | `TestTC21_AdminLogin` |
| TC-22, TC-23 | `TestTC23_ProductValidation` |
| TC-24, TC-25 | `TestTC24_TC25_Archive` |
| TC-26 | `TestTC26_RegisterMachine` |
| TC-27 | `TestTC27_MachineKeys` |
| TC-28 | `TestTC28_MachineBoundaries` |
| TC-29, TC-30 | `TestTC29_TC30_Recap` |
| TC-31 | `TestTC31_ChangeLog` |
| TC-33 | `TestTC33_LastContact` |
| TC-37, BR-14 | `TestTC37_AgentRestart` |
| NFR-05 | `TestReconcileAfterFlows`, `TestSeedDemo` |

TC-02, TC-07 and TC-32 are screen behaviour, and TC-34 to TC-36 need the motor board; they belong to the kiosk and agent tests.

## Project layout

```
cmd/vending-server/      main: serve and the command-line tools
internal/config/         settings from the environment and .env
internal/database/       connection, embedded migrations (migrations/0001_init.sql)
internal/core/           the use cases and business rules, one file per area
internal/httpapi/        routes, middleware, JSON errors, and the integration tests
internal/security/       Argon2id passwords, tokens, UUIDs
internal/seed/           demo data, simulated event by event so it reconciles
web/admin/               the admin web, embedded into the binary
deploy/                  deploy.sh (run on your laptop) and setup-vm.sh (run on the VM); see DEPLOY.md
```

## Differences from the PRD data dictionary

- **`sessions`** is a ninth table: admin and operator logins, with only a hash of each token stored. It isn't business data, so the ERD can leave it out or show it as an implementation table.
- **`restock_sessions.client_id`** holds the kiosk's UUID for a visit, so a resend after a network failure is saved once.
- **`audit_logs.entity_label`** keeps the name as it was when the change happened ("VM-01 · Slot 3", "Budi Santoso (budi)"), so the log stays readable after a rename.
- **Indexes**: those the data dictionary lists, plus `(status, machine_id, created_at)` for the per-machine recap and the last sale.
- **Times** are `DATETIME(3)`: milliseconds keep the order of events within a second.

## Next: the Kiosk Agent

The agent serves the kiosk page on the machine's own localhost and forwards each of its calls (kiosk README) to `/api/machine/...` here, adding `X-Machine-Key`; it also passes `/uploads/...` through, since catalog image URLs are relative. For the kiosk's `pay`, it calls `pay` here, turns the coil with `board.py`'s logic, then reports `result` and returns the final state to the page. It never resends a vend command (BR-14). When both run on one computer during development, give them different ports.
