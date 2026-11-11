// Package http exposes the use cases over HTTP:
//
//	/api/admin/...   the admin web, authenticated by an admin session token
//	/api/machine/... the Kiosk Agent, authenticated by its machine key (X-Machine-Key),
//	                 plus an operator session token for the restock endpoints
//	/uploads/...     product images
//	/                the admin web itself (embedded static files)
//
// This is a delivery layer and nothing more: a handler decodes the request,
// calls one usecase, and maps what comes back to a wire shape. No rule is
// decided here. Two things in particular belong to this layer and to no other:
// turning a business error code into an HTTP status (status.go), and turning a
// domain value into JSON field names (the dto_* files).
//
// The package is named http and imports net/http, which is legal because a
// package's own name is not in scope inside it. Importers alias it, by the
// convention the rest of the tree already uses: deliveryhttp.
package http

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"vending/internal/core"
	audituc "vending/internal/usecase/audit"
	"vending/internal/usecase/auth"
	"vending/internal/usecase/catalog"
	machineuc "vending/internal/usecase/machine"
	"vending/internal/usecase/operator"
	"vending/internal/usecase/product"
	"vending/internal/usecase/purchase"
	"vending/internal/usecase/recap"
	"vending/internal/usecase/restock"
)

// Server routes requests to the usecases.
type Server struct {
	svc  *core.Service
	uc   UseCases
	log  *slog.Logger
	mux  *http.ServeMux
	web  fs.FS
	opts Options
}

// UseCases are the usecase ports this layer depends on. cmd/vending-server
// builds the concrete ones and injects them, and a handler test passes mocks.
// It grows as each slice moves out of core, and when core is empty it is the
// only dependency left (docs/REARCHITECTURE.md).
type UseCases struct {
	Audit    audituc.UseCase
	Auth     auth.UseCase
	Catalog  catalog.UseCase
	Machine  machineuc.UseCase
	Operator operator.UseCase
	Product  product.UseCase
	Purchase purchase.UseCase
	Recap    recap.UseCase
	Restock  restock.UseCase
}

// Options are the server's settings and optional features.
type Options struct {
	// UploadDir is where product images are stored; the server serves them at
	// /uploads/.
	UploadDir string

	// DemoReset, when set, enables POST /api/admin/demo/reset: delete every row and
	// load the demo data again. Only for demo servers (DEMO_MODE=true).
	DemoReset func(ctx context.Context) error
}

// New returns the HTTP handler. web holds the admin web files (index.html at its root).
func New(svc *core.Service, uc UseCases, web fs.FS, log *slog.Logger, opts Options) http.Handler {
	s := &Server{svc: svc, uc: uc, log: log, mux: http.NewServeMux(), web: web, opts: opts}
	s.routes()
	return s.withMiddleware(http.HandlerFunc(s.dispatch))
}

// dispatch answers unknown /api/ paths with JSON instead of the mux's plain-text 404 and 405.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if _, pattern := s.mux.Handler(r); pattern == "" || pattern == "GET /" {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "Alamat API tidak ditemukan: " + r.Method + " " + r.URL.Path})
			return
		}
	}
	s.mux.ServeHTTP(w, r) // the mux itself must route, so it fills in path values such as {id}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.svc.Ping(ctx); err != nil {
		s.log.Error("health check: database unreachable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "database": "unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "database": "ok", "serverTime": time.Now().UnixMilli()})
}
