package http

import (
	"net/http"

	"vending/internal/model"
)

// The staff accounts an admin manages (UC-12). Admin accounts are not reachable
// from here at all: they are made and repaired from the command line, and the
// usecase refuses them.

func (s *Server) operators(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	list, err := s.uc.Operator.List(r.Context())
	if err != nil {
		return nil, err
	}
	return operatorViews(list), nil
}

func (s *Server) createOperator(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	var b operatorBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	id, err := s.uc.Operator.Create(r.Context(), p, b.input())
	if err != nil {
		return nil, err
	}
	return created{createdIDDTO{ID: id}}, nil
}

func (s *Server) setOperatorActive(active bool) adminFunc {
	return func(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return nil, err
		}
		return ok(), s.uc.Operator.SetActive(r.Context(), p, id, active)
	}
}

func (s *Server) resetOperatorPassword(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	pw, err := s.uc.Operator.ResetPassword(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	return temporaryPasswordDTO{TemporaryPassword: pw}, nil
}
