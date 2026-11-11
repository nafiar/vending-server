// Package restock reads and writes the record of operator visits. The port is
// here; the drivers live in subpackages.
//
// A visit is the only evidence behind the counts this system reports, so it is
// written once and never changed: there is no update and no delete here. A
// mistake at a machine is corrected by the next visit, which leaves both facts
// in the record.
package restock

import (
	"context"

	"vending/internal/model"
)

//go:generate mockgen -source=restock.go -destination=mock/restock_mock.go -package=mock

// Repository is the restock_sessions and restock_items tables as the rules see
// them. The two are one port because a session without its lines is not a
// visit: nothing may write one without the other.
type Repository interface {
	// ByClientID returns the visit saved under the id the kiosk generated, or
	// an error wrapping model.ErrNoRecord. It is how a resend is recognised
	// instead of being saved twice.
	ByClientID(ctx context.Context, clientID string) (model.RestockSession, error)

	// Create saves the visit and returns its id. It returns an error wrapping
	// model.ErrDuplicateID when the client id was taken between the read above
	// and this write, which is two sends of one visit racing.
	Create(ctx context.Context, s model.RestockSession) (int64, error)

	// AddItems writes the per-slot lines of one visit, all of them in one
	// statement: they are a single fact about the visit, not a list of
	// independent ones.
	AddItems(ctx context.Context, sessionID int64, items []model.RestockItem) error
}
