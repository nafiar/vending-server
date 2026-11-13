// Package audit reads the change log (UC-12, FR-26).
//
// There is no write here. Every row in the change log is written by the usecase
// that made the change, inside the same unit of work, which is the only way the
// log cannot miss one (repository/audit). This package is the screen that reads
// them back, and keeping it read-only is deliberate: a change log with a second
// way in is a change log you have to go and check.
//
// The default span is wide — four months — because the question this screen
// answers is usually "when did this change, and who changed it", and that is
// asked long after the change.
package audit

import (
	"context"
	"log/slog"
	"time"

	"vending/internal/common/calendar"
	"vending/internal/common/clock"
	"vending/internal/model"
	"vending/internal/repository/audit"
)

//go:generate mockgen -source=audit.go -destination=mock/audit_mock.go -package=mock

// The spans the screen may ask for: four months by default, two years at most,
// the same ceiling the recap has.
const (
	defaultDays  = 120
	maxRangeDays = 731
)

// Query is a request for the change log. From and To are local calendar days
// (YYYY-MM-DD); empty means the default span ending today. EntityType is one of
// PRODUCT, MACHINE, SLOT or USER, or empty for every type.
type Query struct {
	From, To   string
	EntityType string
}

// UseCase is the port the delivery layer depends on.
type UseCase interface {
	// Logs is the change log over the query's span, newest first.
	Logs(ctx context.Context, q Query) ([]model.AuditLog, error)
}

// Service reads the change log.
type Service struct {
	logs  audit.Reader
	clock clock.Clock
	loc   *time.Location
	log   *slog.Logger
}

// Compile-time proof that the service still answers the port.
var _ UseCase = (*Service)(nil)

// New returns the usecase. loc is the zone the From/To days are read in, so
// that "2026-10-22" means that day in Jakarta and not in UTC.
func New(logs audit.Reader, cl clock.Clock, loc *time.Location, log *slog.Logger) *Service {
	return &Service{logs: logs, clock: cl, loc: loc, log: log}
}

func (s *Service) Logs(ctx context.Context, q Query) ([]model.AuditLog, error) {
	r, err := calendar.ParseRange(s.clock.Now(), s.loc, q.From, q.To, defaultDays, maxRangeDays)
	if err != nil {
		return nil, err
	}
	if q.EntityType != "" && !model.AuditEntityTypes[q.EntityType] {
		return nil, model.FieldErrors{"entityType": "Jenis harus PRODUCT, MACHINE, SLOT atau USER."}.Err()
	}
	return s.logs.List(ctx, model.AuditFilter{
		Start: r.Start, End: r.End, EntityType: q.EntityType, Limit: model.MaxAuditRows,
	})
}
