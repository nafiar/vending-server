package http

import (
	"net/http"

	"vending/internal/model"
)

// The revenue figures the admin home and the recap screen show (UC-06).

func (s *Server) summary(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	machineID := q.int("machineId")
	if err := q.err(); err != nil {
		return nil, err
	}
	sum, err := s.uc.Recap.Summary(r.Context(), machineID)
	if err != nil {
		return nil, err
	}
	return summaryView(sum), nil
}

func (s *Server) recap(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	in := recapQuery(q)
	if err := q.err(); err != nil {
		return nil, err
	}
	rec, err := s.uc.Recap.Recap(r.Context(), in)
	if err != nil {
		return nil, err
	}
	return recapView(rec), nil
}
