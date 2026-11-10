package http

import (
	"net/http"

	"vending/internal/model"
)

// What a kiosk shows, and the agent's heartbeat (UC-01).

func (s *Server) catalog(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	c, err := s.uc.Catalog.Get(r.Context(), m)
	if err != nil {
		return nil, err
	}
	return catalogView(c), nil
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	h, err := s.uc.Catalog.Heartbeat(r.Context(), m)
	if err != nil {
		return nil, err
	}
	return heartbeatView(h), nil
}
