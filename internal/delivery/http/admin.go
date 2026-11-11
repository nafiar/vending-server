package http

import (
	"net/http"
	"strconv"

	"vending/internal/core"
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

func (s *Server) summary(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	machineID := q.int("machineId")
	if err := q.err(); err != nil {
		return nil, err
	}
	return s.svc.GetSummary(r.Context(), machineID)
}

func (s *Server) recap(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	in := core.RecapQuery{
		From: q.str("from"), To: q.str("to"), MachineID: q.int("machineId"),
		GroupBy: q.str("groupBy"), Breakdown: q.str("breakdown"),
	}
	if err := q.err(); err != nil {
		return nil, err
	}
	return s.svc.GetRecap(r.Context(), in)
}

func (s *Server) transactions(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	in := core.TransactionQuery{
		From: q.str("from"), To: q.str("to"), MachineID: q.int("machineId"), SlotNumber: int(q.int("slotNumber")),
		Status: q.str("status"), Flag: q.str("flag"), Page: int(q.int("page")), PageSize: int(q.int("pageSize")), All: q.bool("all"),
	}
	if err := q.err(); err != nil {
		return nil, err
	}
	return s.svc.ListTransactions(r.Context(), in)
}

func (s *Server) restockSessions(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	in := core.RestockQuery{
		From: q.str("from"), To: q.str("to"), MachineID: q.int("machineId"), UserID: q.int("userId"),
		Page: int(q.int("page")), PageSize: int(q.int("pageSize")), All: q.bool("all"),
	}
	if err := q.err(); err != nil {
		return nil, err
	}
	return s.svc.ListRestockSessions(r.Context(), in)
}

func (s *Server) machines(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	list, err := s.svc.ListMachines(r.Context())
	if list == nil {
		list = []core.MachineView{}
	}
	return list, err
}

func (s *Server) machine(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.svc.GetMachine(r.Context(), id)
}

func (s *Server) createMachine(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	var in core.MachineInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	v, err := s.svc.CreateMachine(r.Context(), p, in)
	return created{v}, err
}

func (s *Server) setMachineActive(active bool) adminFunc {
	return func(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return nil, err
		}
		return s.svc.SetMachineActive(r.Context(), p, id, active)
	}
}

func (s *Server) rotateMachineKey(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	return s.svc.RotateMachineKey(r.Context(), p, id)
}

func (s *Server) updateSlot(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return nil, model.NotFound("Slot tidak ditemukan.")
	}
	var in core.SlotInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.UpdateSlot(r.Context(), p, id, n, in)
}

func (s *Server) operators(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	list, err := s.svc.ListOperators(r.Context())
	if list == nil {
		list = []core.OperatorView{}
	}
	return list, err
}

func (s *Server) createOperator(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	var in core.OperatorInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	v, err := s.svc.CreateOperator(r.Context(), p, in)
	return created{v}, err
}

func (s *Server) setOperatorActive(active bool) adminFunc {
	return func(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, s.svc.SetOperatorActive(r.Context(), p, id, active)
	}
}

func (s *Server) resetOperatorPassword(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	pw, err := s.svc.ResetOperatorPassword(r.Context(), p, id)
	return map[string]string{"temporaryPassword": pw}, err
}

func (s *Server) auditLogs(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	q := newQuery(r)
	return s.svc.ListAuditLogs(r.Context(), core.AuditQuery{From: q.str("from"), To: q.str("to"), EntityType: q.str("entityType")})
}

func (s *Server) reconcile(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	rows, err := s.svc.Reconcile(r.Context())
	if err != nil {
		return nil, err
	}
	mismatches := 0
	for _, x := range rows {
		if !x.Matches {
			mismatches++
		}
	}
	return map[string]any{"slots": rows, "mismatches": mismatches}, nil
}
