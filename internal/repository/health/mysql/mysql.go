// Package mysql is the health driver.
package mysql

import (
	"context"
	"database/sql"

	"vending/internal/repository/health"
)

// MySQL checks the connection pool.
//
// It takes the *sql.DB and not a Queryer, because the question is about the
// pool itself rather than about anything in it.
type MySQL struct{ db *sql.DB }

// Compile-time proof that the driver still matches the port.
var _ health.Repository = (*MySQL)(nil)

// New returns the driver over the pool.
func New(db *sql.DB) *MySQL { return &MySQL{db: db} }

func (s *MySQL) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
