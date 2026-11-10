// Package core holds the business rules of the vending information system:
// every use case in PRD v3, written against MySQL with database/sql.
//
// One Service serves both sides of the system:
//   - the admin web (UC-06 to UC-12): recap, histories, products, machines, operators, change log;
//   - the Kiosk Agent on each machine (UC-01 to UC-05): catalog, purchases, operator restock visits.
//
// Every write that changes what an admin manages also writes its audit_logs row
// in the same database transaction, so the change log can never miss a change.
package core

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"vending/internal/common/clock"
	"vending/internal/repository/imagestore"
	"vending/internal/repository/mysqldb"
)

// Options configure a Service.
type Options struct {
	Location      *time.Location        // calendar days and hours for recaps; Asia/Jakarta (WIB)
	Images        imagestore.Repository // where product images are stored
	AdminIdle     time.Duration         // admin session idle timeout (BR-13: 30 min)
	OperatorIdle  time.Duration         // operator session idle timeout (BR-13: 2 min)
	PaymentWindow time.Duration         // how long a PENDING transaction can be paid (BR-06: 60 s)
	Now           func() time.Time
	Logger        *slog.Logger
}

// Service implements the use cases.
type Service struct {
	db            *sql.DB
	tx            *mysqldb.Runner
	loc           *time.Location
	images        imagestore.Repository
	adminIdle     time.Duration
	operatorIdle  time.Duration
	paymentWindow time.Duration
	clock         clock.Clock
	log           *slog.Logger
}

// New returns a Service. Zero options get the PRD defaults.
func New(db *sql.DB, o Options) *Service {
	s := &Service{
		db:            db,
		tx:            mysqldb.NewRunner(db, o.Logger),
		loc:           o.Location,
		images:        o.Images,
		adminIdle:     o.AdminIdle,
		operatorIdle:  o.OperatorIdle,
		paymentWindow: o.PaymentWindow,
		clock:         clock.New(o.Now),
		log:           o.Logger,
	}
	if s.loc == nil {
		s.loc, _ = time.LoadLocation("Asia/Jakarta")
	}
	if s.adminIdle <= 0 {
		s.adminIdle = 30 * time.Minute
	}
	if s.operatorIdle <= 0 {
		s.operatorIdle = 2 * time.Minute
	}
	if s.paymentWindow <= 0 {
		s.paymentWindow = 60 * time.Second
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// DB exposes the pool, for the command-line tools.
func (s *Service) DB() *sql.DB { return s.db }

// Location is the time zone used for calendar days.
func (s *Service) Location() *time.Location { return s.loc }

// now is the current time in UTC, cut to the milliseconds DATETIME(3) stores.
func (s *Service) now() time.Time { return s.clock.Now() }

// Now is the service's clock, for tools that write data relative to it (the demo seed).
func (s *Service) Now() time.Time { return s.now() }

// Ping checks the database, for the health endpoint.
func (s *Service) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// withTx runs fn in one database transaction. The retry InnoDB needs lives in
// mysqldb.Runner, not in a business rule.
func (s *Service) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return s.tx.Do(ctx, fn)
}

// ---------------------------------------------------------------- small helpers

// These forward to mysqldb so there is one implementation; the call sites move
// to the repository adapters as each slice lands.

func ms(t time.Time) int64                { return mysqldb.Ms(t) }
func nullMs(t sql.NullTime) *int64        { return mysqldb.NullMs(t) }
func nullString(s sql.NullString) *string { return mysqldb.NullString(s) }
func nullInt(n sql.NullInt64) *int64      { return mysqldb.NullInt(n) }
func strOrNil(s string) any               { return mysqldb.StrOrNil(s) }
func placeholders(n int) string           { return mysqldb.Placeholders(n) }
func int64sToArgs(ids []int64) []any      { return mysqldb.Args(ids) }
