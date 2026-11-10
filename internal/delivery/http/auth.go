package http

import (
	"net/http"
	"strings"

	"vending/internal/model"
)

// The handler adapters. Each one answers the question "who may call this?"
// before the handler runs, so no handler repeats the check and none can forget
// it: the wrapper a route is given in router.go is its access rule.

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

type adminFunc func(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error)

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

type machineFunc func(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error)

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

type operatorFunc func(w http.ResponseWriter, r *http.Request, m model.Machine, p model.Principal) (any, error)

func (s *Server) operatorOnly(fn operatorFunc) http.Handler {
	return s.machineOnly(func(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
		p, err := s.svc.OperatorSession(r.Context(), m, bearer(r))
		if err != nil {
			return nil, err
		}
		return fn(w, r, m, p)
	})
}
