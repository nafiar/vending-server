// Package user reads and writes the staff accounts. The port is here; the
// drivers live in subpackages.
//
// The lockout counter (FR-13) is why this port has a lock method. Reading the
// counter and raising it have to be one step: five wrong passwords sent at once
// must count as five, not as one, or the lockout can be walked straight past.
package user

import (
	"context"
	"time"

	"vending/internal/model"
)

//go:generate mockgen -source=user.go -destination=mock/user_mock.go -package=mock

// Repository is the users table as the rules see it. Methods grow with the
// slices that need them; a port carries only what a usecase actually calls.
type Repository interface {
	// LockForUpdate returns the account with this username and holds an
	// exclusive lock on its row until the unit of work ends, so the lockout
	// counter can be read and written as one step.
	//
	// The username is matched as given; lower-casing it is the caller's rule,
	// not the table's. Returns an error wrapping model.ErrNoRecord when there is
	// no such account.
	LockForUpdate(ctx context.Context, username string) (model.User, error)

	// SetFailedLogins records how many wrong passwords this account has in a
	// row, and clears any lock it had.
	SetFailedLogins(ctx context.Context, id int64, failed int) error

	// LockUntil shuts the account until the given time and resets the counter,
	// so the next wrong password after it reopens starts a fresh five (FR-13).
	LockUntil(ctx context.Context, id int64, until time.Time) error

	// ClearFailedLogins forgets the wrong passwords and any lock, after one
	// that was right.
	ClearFailedLogins(ctx context.Context, id int64) error

	// FirstActiveAdminID is the lowest-numbered active admin, or an error
	// wrapping model.ErrNoRecord when there is none.
	//
	// It exists for the command-line tools: a change still has to be
	// attributable to a person in the log (FR-26), and on the command line
	// there is nobody signed in to attribute it to.
	FirstActiveAdminID(ctx context.Context) (int64, error)
}
