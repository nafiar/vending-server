package audit_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"vending/internal/common/clock"
	"vending/internal/model"
	mock "vending/internal/repository/audit/mock"
	"vending/internal/usecase/audit"
)

// The change-log read decides three things: how far back to look by default,
// what to refuse, and how many rows a person may be handed at once.

var (
	jakarta = func() *time.Location {
		loc, err := time.LoadLocation("Asia/Jakarta")
		if err != nil {
			panic(err)
		}
		return loc
	}()
	// 22 Oct 2026, 10:00 WIB.
	t0 = time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
)

func newFixture(t *testing.T) (*mock.MockReader, *audit.Service, *gomock.Controller) {
	ctrl := gomock.NewController(t)
	logs := mock.NewMockReader(ctrl)
	return logs, audit.New(logs, clock.New(func() time.Time { return t0 }), jakarta, nil), ctrl
}

// The default span is four months back, in the server's own zone, and the cap
// travels with the filter rather than living in the query.
func TestLogsDefaultSpanAndCap(t *testing.T) {
	logs, uc, ctrl := newFixture(t)
	defer ctrl.Finish()

	logs.EXPECT().List(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, f model.AuditFilter) ([]model.AuditLog, error) {
			wantStart := time.Date(2026, 10, 22, 0, 0, 0, 0, jakarta).AddDate(0, 0, -119).UTC()
			wantEnd := time.Date(2026, 10, 23, 0, 0, 0, 0, jakarta).UTC()
			if !f.Start.Equal(wantStart) || !f.End.Equal(wantEnd) {
				t.Errorf("span = %s..%s, want %s..%s", f.Start, f.End, wantStart, wantEnd)
			}
			if f.Limit != model.MaxAuditRows {
				t.Errorf("limit = %d, want %d", f.Limit, model.MaxAuditRows)
			}
			if f.EntityType != "" {
				t.Errorf("entity type = %q, want every type", f.EntityType)
			}
			return nil, nil
		})

	if _, err := uc.Logs(context.Background(), audit.Query{}); err != nil {
		t.Fatal(err)
	}
}

// An entity type nobody knows is refused, and the repository is never asked:
// showing every change on a screen that says it is showing product changes
// would be worse than an error.
func TestLogsRefusesAnUnknownEntityType(t *testing.T) {
	_, uc, ctrl := newFixture(t)
	defer ctrl.Finish()
	// No EXPECT on the reader.

	_, err := uc.Logs(context.Background(), audit.Query{EntityType: "INVOICE"})
	e, ok := model.AsError(err)
	if !ok || e.Code != model.CodeValidation {
		t.Fatalf("err = %v, want a VALIDATION error", err)
	}
	if _, ok := e.Fields["entityType"]; !ok {
		t.Fatalf("fields = %v, want one on entityType", e.Fields)
	}
}

func TestLogsPassesAKnownEntityType(t *testing.T) {
	logs, uc, ctrl := newFixture(t)
	defer ctrl.Finish()

	logs.EXPECT().List(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, f model.AuditFilter) ([]model.AuditLog, error) {
			if f.EntityType != model.EntitySlot {
				t.Errorf("entity type = %q, want %q", f.EntityType, model.EntitySlot)
			}
			return []model.AuditLog{{ID: 1}}, nil
		})

	out, err := uc.Logs(context.Background(), audit.Query{EntityType: model.EntitySlot})
	if err != nil || len(out) != 1 {
		t.Fatalf("got %d rows, %v", len(out), err)
	}
}
