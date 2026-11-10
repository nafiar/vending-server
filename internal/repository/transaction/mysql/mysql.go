// Package mysql is the transactions driver.
package mysql

import (
	"context"
	"time"

	"vending/internal/model"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/transaction"
)

// MySQL reads and writes the transactions table.
type MySQL struct{ q mysqldb.Queryer }

// Compile-time proof that the driver still matches the port.
var _ transaction.Repository = (*MySQL)(nil)

// New returns the driver over q, which is either the pool or one transaction.
func New(q mysqldb.Queryer) *MySQL { return &MySQL{q: q} }

func (s *MySQL) CountOpenBySlot(ctx context.Context, machineID int64, pendingSince time.Time) (map[int64]int, error) {
	rows, err := s.q.QueryContext(ctx, `
		SELECT slot_id, COUNT(*) FROM transactions
		WHERE machine_id = ? AND (status = ? OR (status = ? AND created_at > ?))
		GROUP BY slot_id`,
		machineID, model.StatusPaid, model.StatusPending, pendingSince)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var slotID int64
		var n int
		if err := rows.Scan(&slotID, &n); err != nil {
			return nil, err
		}
		out[slotID] = n
	}
	return out, rows.Err()
}
