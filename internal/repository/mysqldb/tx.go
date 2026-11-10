package mysqldb

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Queryer is what *sql.DB and *sql.Tx have in common. A repository adapter
// holds one, so the same adapter type serves a direct read off the pool and a
// call inside a unit of work.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// The MySQL error numbers the repositories react to.
const (
	numDuplicateKey = 1062
	numLockWait     = 1205
	numDeadlock     = 1213
)

func isNumber(err error, number uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == number
}

// IsDuplicateKey reports whether err is a unique-key violation. A repository
// turns it into the business meaning its caller expects (an id already used, a
// machine code already taken), so no usecase reads a driver error.
func IsDuplicateKey(err error) bool { return isNumber(err, numDuplicateKey) }

// isRetryable reports whether InnoDB aborted the transaction for a reason that
// disappears on a second attempt.
func isRetryable(err error) bool { return isNumber(err, numDeadlock) || isNumber(err, numLockWait) }

const maxAttempts = 4

// Runner runs work inside a database transaction. It is the only place that
// knows a transaction can be aborted and retried, so no rule above it does.
type Runner struct {
	db  *sql.DB
	log *slog.Logger
}

// NewRunner returns the runner over this pool.
func NewRunner(db *sql.DB, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{db: db, log: log}
}

// DB exposes the pool, for a direct read that needs no transaction.
func (r *Runner) DB() *sql.DB { return r.db }

// Do runs fn in a transaction and commits it when fn returns nil.
//
// InnoDB may abort one of two transactions that lock the same rows in opposite
// order (a deadlock) or time out waiting on a lock; both are retried, so fn
// must not keep state from a failed attempt.
func (r *Runner) Do(ctx context.Context, fn func(tx *sql.Tx) error) error {
	for attempt := 1; ; attempt++ {
		err := r.once(ctx, nil, fn)
		if err != nil && attempt < maxAttempts && isRetryable(err) {
			r.log.Warn("retrying database transaction", "attempt", attempt, "err", err)
			time.Sleep(time.Duration(attempt*25) * time.Millisecond)
			continue
		}
		return err
	}
}

// Read runs fn in a read-only transaction, so every statement in it sees one
// snapshot. Nothing is written, so there is nothing to retry.
func (r *Runner) Read(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return r.once(ctx, &sql.TxOptions{ReadOnly: true}, fn)
}

func (r *Runner) once(ctx context.Context, opts *sql.TxOptions, fn func(tx *sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
