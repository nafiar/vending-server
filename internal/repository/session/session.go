// Package session reads and writes the signed-in sessions. The port is here;
// the drivers live in subpackages.
//
// A session is identified by the hash of its token, never by the token: the
// caller hashes what it was sent and looks that up, so what is stored cannot be
// replayed as a token if the table is ever read by someone who should not.
package session

import (
	"context"
	"time"

	"vending/internal/model"
)

//go:generate mockgen -source=session.go -destination=mock/session_mock.go -package=mock

// Repository is the sessions table as the rules see it.
type Repository interface {
	// Create stores a new session.
	Create(ctx context.Context, s model.Session) error

	// ByTokenHash returns the session with this token hash and kind, together
	// with who it belongs to and whether that account is still active, or an
	// error wrapping model.ErrNoRecord.
	//
	// The kind is part of the lookup rather than something the caller checks
	// afterwards, so an operator token can never be presented as an admin one.
	ByTokenHash(ctx context.Context, kind, tokenHash string) (model.AuthSession, error)

	// TouchSeen records that the session was just used, which is what the idle
	// timeout is measured from (BR-13).
	TouchSeen(ctx context.Context, id int64, at time.Time) error

	// Delete ends one session by id.
	Delete(ctx context.Context, id int64) error

	// DeleteByTokenHash ends the session with this token hash. A token that is
	// not there is not an error: signing out twice is not a failure.
	DeleteByTokenHash(ctx context.Context, tokenHash string) error
}
