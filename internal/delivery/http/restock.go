package http

import (
	"net/http"

	"vending/internal/core"
	"vending/internal/model"
)

// The operator's visit to a machine (UC-04, UC-05): they sign in on the kiosk
// itself, so these are machine endpoints carrying a second, operator token.

func (s *Server) operatorLogin(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	var b loginBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	l, err := s.uc.Auth.OperatorLogin(r.Context(), m, b.Username, b.Password)
	if err != nil {
		return nil, err
	}
	return loginView(l), nil
}

func (s *Server) operatorLogout(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	return ok(), s.uc.Auth.Logout(r.Context(), bearer(r))
}

func (s *Server) operatorSlots(w http.ResponseWriter, r *http.Request, m model.Machine, p model.Principal) (any, error) {
	return s.svc.OperatorSlots(r.Context(), m)
}

func (s *Server) saveRestock(w http.ResponseWriter, r *http.Request, m model.Machine, p model.Principal) (any, error) {
	var in core.RestockInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.SaveRestock(r.Context(), m, p, in)
}
