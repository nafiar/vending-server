package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"go.uber.org/mock/gomock"

	deliveryhttp "vending/internal/delivery/http"
	"vending/internal/model"
	auditmock "vending/internal/usecase/audit/mock"
	authmock "vending/internal/usecase/auth/mock"
	maintenancemock "vending/internal/usecase/maintenance/mock"
	"vending/internal/usecase/recap"
	recapmock "vending/internal/usecase/recap/mock"
)

// These tests are the delivery layer on its own, with every usecase mocked and
// no database anywhere. That is the point of the layer having ports: what is
// checked here is the three things this layer actually decides --
//
//   - who is allowed to call a route, and what happens when they are not;
//   - which HTTP status a business error code becomes;
//   - which JSON field names a domain value is written out as --
//
// and none of them need a row in a table to test. The end-to-end suite in
// test/api still drives the real thing over real MySQL; these catch the
// mistakes that live in this layer, at the speed of a unit test.

// fixture is a server with every usecase mocked.
type fixture struct {
	auth        *authmock.MockUseCase
	audit       *auditmock.MockUseCase
	recap       *recapmock.MockUseCase
	maintenance *maintenancemock.MockUseCase
	srv         *httptest.Server
}

func newFixture(t *testing.T) (*fixture, *gomock.Controller) {
	t.Helper()
	ctrl := gomock.NewController(t)
	f := &fixture{
		auth:        authmock.NewMockUseCase(ctrl),
		audit:       auditmock.NewMockUseCase(ctrl),
		recap:       recapmock.NewMockUseCase(ctrl),
		maintenance: maintenancemock.NewMockUseCase(ctrl),
	}
	web := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>admin</title>")}}
	h := deliveryhttp.New(deliveryhttp.UseCases{
		Auth:        f.auth,
		Audit:       f.audit,
		Recap:       f.recap,
		Maintenance: f.maintenance,
	}, web, slog.New(slog.NewTextHandler(io.Discard, nil)), deliveryhttp.Options{})
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	return f, ctrl
}

// get makes a request, with an admin bearer token unless token is empty.
func (f *fixture) get(t *testing.T, path, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), "GET", f.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, body
}

var admin = model.Principal{UserID: 1, Name: "Admin Test", Username: "admin", Role: model.RoleAdmin}

// ---------------------------------------------------------------- access

// An admin route asks usecase/auth about the token before the handler runs, and
// a refusal never reaches the handler at all: no usecase below is mocked here,
// so if one were called the test would fail on an unexpected call.
func TestAdminRouteRefusesABadToken(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.auth.EXPECT().AdminSession(gomock.Any(), "nope").
		Return(model.Principal{}, model.Unauthorized("Sesi berakhir. Masuk lagi."))

	status, body := f.get(t, "/api/admin/audit-logs", "nope")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", status, body)
	}
	if code := codeOf(t, body); code != model.CodeUnauthorized {
		t.Fatalf("code = %s, want %s", code, model.CodeUnauthorized)
	}
}

// The credential is read from the header by this layer, which is the only part
// of the decision it owns.
func TestAdminRouteForwardsTheBearerToken(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.auth.EXPECT().AdminSession(gomock.Any(), "tok-123").Return(admin, nil)
	f.audit.EXPECT().Logs(gomock.Any(), gomock.Any()).Return(nil, nil)

	if status, body := f.get(t, "/api/admin/audit-logs", "tok-123"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
}

// ---------------------------------------------------------------- status mapping

// Every business error code becomes one HTTP status, decided here and nowhere
// else. A handler that returns a VALIDATION error must come out as 422 with the
// per-field messages intact, because that is what the admin web draws next to
// the inputs.
func TestBusinessErrorBecomesItsStatus(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		want  int
		code  string
		field string
	}{
		{
			name: "validation", want: http.StatusUnprocessableEntity, code: model.CodeValidation, field: "entityType",
			err: model.FieldErrors{"entityType": "Jenis harus PRODUCT, MACHINE, SLOT atau USER."}.Err(),
		},
		{name: "not found", err: model.NotFound("Tidak ada."), want: http.StatusNotFound, code: model.CodeNotFound},
		{name: "forbidden", err: model.Forbidden("Tidak boleh."), want: http.StatusForbidden, code: model.CodeForbidden},
		// Anything that is not a model.Error is a server fault, reported as
		// INTERNAL rather than leaking what broke.
		{name: "a plain error", err: errors.New("the disk is on fire"), want: http.StatusInternalServerError, code: model.CodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, ctrl := newFixture(t)
			defer ctrl.Finish()

			f.auth.EXPECT().AdminSession(gomock.Any(), gomock.Any()).Return(admin, nil)
			f.audit.EXPECT().Logs(gomock.Any(), gomock.Any()).Return(nil, c.err)

			status, body := f.get(t, "/api/admin/audit-logs", "tok")
			if status != c.want {
				t.Fatalf("status = %d, want %d: %s", status, c.want, body)
			}
			if got := codeOf(t, body); got != c.code {
				t.Fatalf("code = %s, want %s", got, c.code)
			}
			if c.field != "" {
				var e struct {
					Fields map[string]string `json:"fields"`
				}
				if err := json.Unmarshal(body, &e); err != nil {
					t.Fatal(err)
				}
				if _, ok := e.Fields[c.field]; !ok {
					t.Fatalf("fields = %v, want one on %q", e.Fields, c.field)
				}
			}
			if c.code == model.CodeInternal && strings.Contains(string(body), "disk is on fire") {
				t.Fatalf("a server fault must not be reported to the caller: %s", body)
			}
		})
	}
}

// ---------------------------------------------------------------- wire shapes

// The field names are the contract the admin web is written against, and this
// layer is where a domain value is given them. The change log is the case worth
// pinning: before is null for a CREATE, and the stored JSON goes out as it was
// written rather than re-encoded.
func TestAuditLogsWireShape(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	at := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC)
	f.auth.EXPECT().AdminSession(gomock.Any(), gomock.Any()).Return(admin, nil)
	f.audit.EXPECT().Logs(gomock.Any(), gomock.Any()).Return([]model.AuditLog{
		{
			ID: 2, At: at, ActorID: 1, ActorName: "Admin Test",
			Action: model.ActionUpdate, EntityType: model.EntityProduct, EntityID: 7,
			EntityLabel: "Gacha Rare",
			Before:      []byte(`{"price":25000}`),
			After:       []byte(`{"price":30000}`),
		},
		{
			ID: 1, At: at, ActorID: 1, ActorName: "Admin Test",
			Action: model.ActionCreate, EntityType: model.EntityProduct, EntityID: 7,
			EntityLabel: "Gacha Rare",
			After:       []byte(`{"name":"Gacha Rare"}`),
		},
	}, nil)

	status, body := f.get(t, "/api/admin/audit-logs", "tok")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	var rows []struct {
		ID        int64 `json:"id"`
		CreatedAt int64 `json:"createdAt"`
		User      struct {
			ID   int64
			Name string
		} `json:"user"`
		Action      string          `json:"action"`
		EntityType  string          `json:"entityType"`
		EntityID    int64           `json:"entityId"`
		EntityLabel string          `json:"entityLabel"`
		Before      json.RawMessage `json:"before"`
		After       json.RawMessage `json:"after"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %s", len(rows), body)
	}
	got := rows[0]
	if got.ID != 2 || got.CreatedAt != at.UnixMilli() || got.User.Name != "Admin Test" ||
		got.Action != "UPDATE" || got.EntityType != "PRODUCT" || got.EntityID != 7 || got.EntityLabel != "Gacha Rare" {
		t.Fatalf("row = %+v", got)
	}
	if string(got.Before) != `{"price":25000}` || string(got.After) != `{"price":30000}` {
		t.Fatalf("before/after were not carried through as stored: %s / %s", got.Before, got.After)
	}
	// A CREATE has no before, and it has to stay distinguishable from an empty one.
	if string(rows[1].Before) != "null" {
		t.Fatalf("before on a CREATE = %s, want null", rows[1].Before)
	}
}

// The query string is this layer's to read, and it reaches the usecase as
// values rather than as text.
func TestTransactionQueryIsRead(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.auth.EXPECT().AdminSession(gomock.Any(), gomock.Any()).Return(admin, nil)
	f.recap.EXPECT().History(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, q recap.HistoryQuery) (model.TransactionHistory, error) {
			want := recap.HistoryQuery{
				MachineID: 3, Status: model.StatusDispensed, Flag: model.FlagProblem,
				Page: 2, PageSize: 10, All: true,
			}
			if q != want {
				t.Errorf("query = %+v, want %+v", q, want)
			}
			return model.TransactionHistory{Page: 2, PageSize: 10, Rows: []model.TransactionHistoryRow{}}, nil
		})

	status, body := f.get(t, "/api/admin/transactions?page=2&pageSize=10&status=DISPENSED&flag=problem&machineId=3&all=true", "tok")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	// An empty page is an empty array, never null: the admin web iterates it.
	if !strings.Contains(string(body), `"rows":[]`) {
		t.Fatalf("empty rows must be [], got %s", body)
	}
}

// A number that is not a number is refused rather than read as zero, because a
// filter silently read as 0 shows an admin the wrong figures and says nothing.
func TestBadQueryNumberIsAValidationError(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	f.auth.EXPECT().AdminSession(gomock.Any(), gomock.Any()).Return(admin, nil)
	// No EXPECT on recap: the request must not reach the usecase.

	status, body := f.get(t, "/api/admin/transactions?machineId=abc", "tok")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", status, body)
	}
	if got := codeOf(t, body); got != model.CodeValidation {
		t.Fatalf("code = %s, want %s", got, model.CodeValidation)
	}
}

// ---------------------------------------------------------------- health and 404

// The health endpoint is the one route that reports a dependency rather than a
// business answer, so an unreachable database is 503 and not 500.
func TestHealthReportsTheDatabase(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.maintenance.EXPECT().Ping(gomock.Any()).Return(nil)

		status, body := f.get(t, "/api/health", "")
		if status != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
			t.Fatalf("status = %d, body = %s", status, body)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		f, ctrl := newFixture(t)
		defer ctrl.Finish()
		f.maintenance.EXPECT().Ping(gomock.Any()).Return(errors.New("connection refused"))

		status, body := f.get(t, "/api/health", "")
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503: %s", status, body)
		}
		if !strings.Contains(string(body), `"database":"unreachable"`) {
			t.Fatalf("body = %s", body)
		}
	})
}

// An unknown /api/ path answers in JSON, because its caller is a program that
// parses JSON. The mux's own plain-text 404 would arrive as an unparseable body
// at the one moment a client most needs to be told what went wrong.
func TestUnknownAPIPathIsJSON(t *testing.T) {
	f, ctrl := newFixture(t)
	defer ctrl.Finish()

	status, body := f.get(t, "/api/admin/nothing-here", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", status, body)
	}
	if got := codeOf(t, body); got != model.CodeNotFound {
		t.Fatalf("code = %s, want %s: %s", got, model.CodeNotFound, body)
	}
}

// codeOf reads the error code out of a JSON error body.
func codeOf(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return e.Code
}
