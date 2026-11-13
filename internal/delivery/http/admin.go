package http

import (
	"net/http"

	"vending/internal/model"
)

// Admin endpoints. The request and response shapes are the contract in
// admin/README.md, which the admin web's js/api.js already calls.

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) (any, error) {
	var b loginBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	l, err := s.uc.Auth.AdminLogin(r.Context(), b.Username, b.Password)
	if err != nil {
		return nil, err
	}
	return loginView(l), nil
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) (any, error) {
	return ok(), s.uc.Auth.Logout(r.Context(), bearer(r))
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	return meView(p, s.opts.DemoReset != nil), nil
}

// demoReset wipes the database and loads the demo data again. Every session
// ends with it, so the admin web goes back to the login screen.
func (s *Server) demoReset(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	if s.opts.DemoReset == nil {
		return nil, model.NotFound("Reset hanya tersedia di server demo (DEMO_MODE=true).")
	}
	s.log.Warn("demo data reset from the admin web", "by", p.Username)
	if err := s.opts.DemoReset(r.Context()); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

// reconcile is the bookkeeping check an admin can run from the web (NFR-05).
// The same check is a command-line subcommand, so what it finds is decided in
// usecase/maintenance and only shaped here.
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	out, err := s.uc.Maintenance.Reconcile(r.Context())
	if err != nil {
		return nil, err
	}
	return reconcileView(out), nil
}
