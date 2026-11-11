package http

import (
	"net/http"

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
	ms, err := s.uc.Restock.Slots(r.Context(), m)
	if err != nil {
		return nil, err
	}
	return operatorSlotsView(ms), nil
}

func (s *Server) saveRestock(w http.ResponseWriter, r *http.Request, m model.Machine, p model.Principal) (any, error) {
	var b restockBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	saved, err := s.uc.Restock.Save(r.Context(), m, p, b.input())
	if err != nil {
		return nil, err
	}
	return restockSavedView(saved), nil
}
