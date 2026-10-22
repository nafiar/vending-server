// Package httpapi exposes the core use cases over HTTP:
//
//	/api/admin/...   the admin web, authenticated by an admin session token
//	/api/machine/... the Kiosk Agent, authenticated by its machine key (X-Machine-Key),
//	                 plus an operator session token for the restock endpoints
//	/uploads/...     product images
//	/                the admin web itself (embedded static files)
package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"vending/internal/core"
)

// Server routes requests to the service.
type Server struct {
	svc *core.Service
	log *slog.Logger
	mux *http.ServeMux
	web fs.FS
}

// New returns the HTTP handler. web holds the admin web files (index.html at its root).
func New(svc *core.Service, web fs.FS, log *slog.Logger) http.Handler {
	s := &Server{svc: svc, log: log, mux: http.NewServeMux(), web: web}
	s.routes()
	return s.withMiddleware(http.HandlerFunc(s.dispatch))
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /api/health", s.health)

	// Admin web (UC-06 to UC-12).
	m.Handle("POST /api/admin/login", s.public(s.adminLogin))
	m.Handle("POST /api/admin/logout", s.public(s.adminLogout))
	m.Handle("GET /api/admin/me", s.admin(s.me))
	m.Handle("GET /api/admin/recap/summary", s.admin(s.summary))
	m.Handle("GET /api/admin/recap", s.admin(s.recap))
	m.Handle("GET /api/admin/transactions", s.admin(s.transactions))
	m.Handle("GET /api/admin/restock-sessions", s.admin(s.restockSessions))
	m.Handle("GET /api/admin/products", s.admin(s.products))
	m.Handle("POST /api/admin/products", s.admin(s.createProduct))
	m.Handle("PUT /api/admin/products/{id}", s.admin(s.updateProduct))
	m.Handle("POST /api/admin/products/{id}/archive", s.admin(s.archiveProduct))
	m.Handle("POST /api/admin/products/{id}/restore", s.admin(s.restoreProduct))
	m.Handle("GET /api/admin/machines", s.admin(s.machines))
	m.Handle("POST /api/admin/machines", s.admin(s.createMachine))
	m.Handle("GET /api/admin/machines/{id}", s.admin(s.machine))
	m.Handle("POST /api/admin/machines/{id}/activate", s.admin(s.setMachineActive(true)))
	m.Handle("POST /api/admin/machines/{id}/deactivate", s.admin(s.setMachineActive(false)))
	m.Handle("POST /api/admin/machines/{id}/rotate-key", s.admin(s.rotateMachineKey))
	m.Handle("PUT /api/admin/machines/{id}/slots/{n}", s.admin(s.updateSlot))
	m.Handle("GET /api/admin/operators", s.admin(s.operators))
	m.Handle("POST /api/admin/operators", s.admin(s.createOperator))
	m.Handle("POST /api/admin/operators/{id}/activate", s.admin(s.setOperatorActive(true)))
	m.Handle("POST /api/admin/operators/{id}/deactivate", s.admin(s.setOperatorActive(false)))
	m.Handle("POST /api/admin/operators/{id}/reset-password", s.admin(s.resetOperatorPassword))
	m.Handle("GET /api/admin/audit-logs", s.admin(s.auditLogs))
	m.Handle("GET /api/admin/reconcile", s.admin(s.reconcile))

	// Kiosk Agent (UC-01 to UC-05). The agent forwards the kiosk's calls here, adding the machine key.
	m.Handle("GET /api/machine/catalog", s.machineOnly(s.catalog))
	m.Handle("POST /api/machine/heartbeat", s.machineOnly(s.heartbeat))
	m.Handle("POST /api/machine/transactions", s.machineOnly(s.createTransaction))
	m.Handle("GET /api/machine/transactions/{id}", s.machineOnly(s.transactionStatus))
	m.Handle("POST /api/machine/transactions/{id}/pay", s.machineOnly(s.pay))
	m.Handle("POST /api/machine/transactions/{id}/result", s.machineOnly(s.result))
	m.Handle("POST /api/machine/transactions/{id}/cancel", s.machineOnly(s.cancel))
	m.Handle("POST /api/machine/transactions/{id}/report", s.machineOnly(s.report))
	m.Handle("POST /api/machine/operator/login", s.machineOnly(s.operatorLogin))
	m.Handle("POST /api/machine/operator/logout", s.machineOnly(s.operatorLogout))
	m.Handle("GET /api/machine/operator/slots", s.operatorOnly(s.operatorSlots))
	m.Handle("POST /api/machine/restock-sessions", s.operatorOnly(s.saveRestock))

	// Files.
	m.Handle("GET /uploads/", http.StripPrefix("/uploads/", uploadsHandler(s.svc.UploadDir())))
	m.Handle("GET /", s.static())
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

// ---------------------------------------------------------------- handler adapters

type replyFunc func(w http.ResponseWriter, r *http.Request) (any, error)

func (s *Server) reply(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if c, ok := v.(created); ok {
		status, v = http.StatusCreated, c.v
	}
	writeJSON(w, status, v)
}

// created marks a reply that made a new record (201).
type created struct{ v any }

func (s *Server) public(fn replyFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := fn(w, r)
		s.reply(w, r, v, err)
	})
}

type adminFunc func(w http.ResponseWriter, r *http.Request, p core.Principal) (any, error)

func (s *Server) admin(fn adminFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := s.svc.AdminSession(r.Context(), bearer(r))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		v, err := fn(w, r, p)
		s.reply(w, r, v, err)
	})
}

type machineFunc func(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error)

func (s *Server) machineOnly(fn machineFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m, err := s.svc.AuthenticateMachine(r.Context(), strings.TrimSpace(r.Header.Get("X-Machine-Key")))
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		v, err := fn(w, r, m)
		s.reply(w, r, v, err)
	})
}

type operatorFunc func(w http.ResponseWriter, r *http.Request, m core.Machine, p core.Principal) (any, error)

func (s *Server) operatorOnly(fn operatorFunc) http.Handler {
	return s.machineOnly(func(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
		p, err := s.svc.OperatorSession(r.Context(), m, bearer(r))
		if err != nil {
			return nil, err
		}
		return fn(w, r, m, p)
	})
}

// ---------------------------------------------------------------- static files

// static serves the admin web. Its files change with each release, so the
// browser revalidates them instead of caching them for long.
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// uploadsHandler serves product images. Every file gets a new random name, so
// it can be cached for a year; directory listings are refused.
func uploadsHandler(dir string) http.Handler {
	files := http.FileServer(noListing{http.Dir(dir)})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		files.ServeHTTP(w, r)
	})
}

type noListing struct{ fs http.FileSystem }

func (n noListing) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}
