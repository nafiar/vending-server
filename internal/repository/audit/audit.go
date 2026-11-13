// Package audit writes and reads the change log. The port is here; the drivers
// live in subpackages.
//
// FR-26 is the reason this package is shaped the way it is: "the change log can
// never miss a change". That is only true if a change and the row recording it
// commit together, so Write is called through uow.Tx, inside the same unit of
// work as the change itself, and never on a pool-bound repository. There is no
// update and no delete here at all: a change log that can be edited is not one.
package audit

import (
	"context"

	"vending/internal/model"
)

//go:generate mockgen -source=audit.go -destination=mock/audit_mock.go -package=mock

// Reader is the change log read back (UC-12).
//
// It is a port of its own, and not a method on Repository, so that the bundle
// in uow.Tx keeps offering writes and nothing else. A usecase that only shows
// the log asks for a Reader, bound to the pool; the one place a row is written
// still reaches it through the unit of work that makes the change.
type Reader interface {
	// List returns the rows in the filter's span, newest first, capped at
	// f.Limit. An empty span is no rows, not an error.
	List(ctx context.Context, f model.AuditFilter) ([]model.AuditLog, error)
}

// Repository is the audit_logs table as the rules see it.
type Repository interface {
	// Write records one change-log row.
	//
	// Call it inside the unit of work that makes the change it describes. A
	// caller that writes the change and then logs it separately has written a
	// log that can be missing rows, which is the one thing FR-26 forbids.
	Write(ctx context.Context, e model.AuditEntry) error
}
