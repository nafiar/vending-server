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
	"strings"
	"time"
)

// Options configure a Service.
type Options struct {
	Location      *time.Location // calendar days and hours for recaps; Asia/Jakarta (WIB)
	UploadDir     string         // where product images are written
	AdminIdle     time.Duration  // admin session idle timeout (BR-13: 30 min)
	OperatorIdle  time.Duration  // operator session idle timeout (BR-13: 2 min)
	PaymentWindow time.Duration  // how long a PENDING transaction can be paid (BR-06: 60 s)
	Now           func() time.Time
	Logger        *slog.Logger
}

// Service implements the use cases.
type Service struct {
	db            *sql.DB
	loc           *time.Location
	uploadDir     string
	adminIdle     time.Duration
	operatorIdle  time.Duration
	paymentWindow time.Duration
	clock         func() time.Time
	log           *slog.Logger
}

// New returns a Service. Zero options get the PRD defaults.
func New(db *sql.DB, o Options) *Service {
	s := &Service{
		db:            db,
		loc:           o.Location,
		uploadDir:     o.UploadDir,
		adminIdle:     o.AdminIdle,
		operatorIdle:  o.OperatorIdle,
		paymentWindow: o.PaymentWindow,
		clock:         o.Now,
		log:           o.Logger,
	}
	if s.loc == nil {
		s.loc, _ = time.LoadLocation("Asia/Jakarta")
	}
	if s.uploadDir == "" {
		s.uploadDir = "data/uploads"
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
	if s.clock == nil {
		s.clock = time.Now
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

// UploadDir is where product images live.
func (s *Service) UploadDir() string { return s.uploadDir }

// now is the current time in UTC, cut to the milliseconds DATETIME(3) stores.
func (s *Service) now() time.Time { return s.clock().UTC().Truncate(time.Millisecond) }

// Now is the service's clock, for tools that write data relative to it (the demo seed).
func (s *Service) Now() time.Time { return s.now() }

// Ping checks the database, for the health endpoint.
func (s *Service) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// withTx runs fn in a database transaction and commits it when fn returns nil.
// InnoDB may abort one of two transactions that lock the same rows in opposite
// order (a deadlock) or time out on a lock; both are retried, so fn must not
// keep state from a failed attempt.
func (s *Service) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	for attempt := 1; ; attempt++ {
		err := s.runTx(ctx, fn)
		if err != nil && attempt < 4 && (isMySQLError(err, mysqlDeadlock) || isMySQLError(err, mysqlLockWait)) {
			s.log.Warn("retrying database transaction", "attempt", attempt, "err", err)
			time.Sleep(time.Duration(attempt*25) * time.Millisecond)
			continue
		}
		return err
	}
}

func (s *Service) runTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- small helpers

func ms(t time.Time) int64 { return t.UnixMilli() }

func nullMs(t sql.NullTime) *int64 {
	if !t.Valid {
		return nil
	}
	v := t.Time.UnixMilli()
	return &v
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullInt(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	v := n.Int64
	return &v
}

func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func int64sToArgs(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}
