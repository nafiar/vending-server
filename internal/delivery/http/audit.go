package http

import (
	"net/http"

	"vending/internal/model"
)

// The change log the admin screen shows (UC-12).

func (s *Server) auditLogs(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	in := auditQuery(q)
	if err := q.err(); err != nil {
		return nil, err
	}
	logs, err := s.uc.Audit.Logs(r.Context(), in)
	if err != nil {
		return nil, err
	}
	return auditView(logs), nil
}
