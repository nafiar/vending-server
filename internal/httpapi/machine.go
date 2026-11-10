package httpapi

import (
	"net/http"

	"vending/internal/core"
	"vending/internal/model"
	"vending/internal/usecase/purchase"
)

// Machine endpoints, called by the Kiosk Agent with its machine key in the
// X-Machine-Key header. The agent forwards the kiosk page's calls
// (kiosk/README.md) to these paths, and reports dispense results itself.

func (s *Server) catalog(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	c, err := s.uc.Catalog.Get(r.Context(), m)
	if err != nil {
		return nil, err
	}
	return catalogView(c), nil
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	h, err := s.uc.Catalog.Heartbeat(r.Context(), m)
	if err != nil {
		return nil, err
	}
	return heartbeatView(h), nil
}

func (s *Server) createTransaction(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var b createTransactionBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	in, err := b.input()
	if err != nil {
		return nil, err
	}
	p, err := s.uc.Purchase.Create(r.Context(), m, in)
	if err != nil {
		return nil, err
	}
	return createdTransactionView(p), nil
}

func (s *Server) transactionStatus(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return state(s.uc.Purchase.Status(r.Context(), m, r.PathValue("id")))
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return state(s.uc.Purchase.Pay(r.Context(), m, r.PathValue("id")))
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var b resultBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	return state(s.uc.Purchase.Result(r.Context(), m, r.PathValue("id"),
		purchase.ResultInput{Status: b.Status, FailureReason: b.FailureReason}))
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var b cancelBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	return state(s.uc.Purchase.Cancel(r.Context(), m, r.PathValue("id"), b.Reason))
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	t, err := s.uc.Purchase.Report(r.Context(), m, r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	return reportResultView(t), nil
}

// state is the answer of every step that reports where a purchase has got to,
// so each of those handlers is one line.
func state(t model.Transaction, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return transactionStateView(t), nil
}

func (s *Server) operatorLogin(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var b loginBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.OperatorLogin(r.Context(), m, b.Username, b.Password)
}

func (s *Server) operatorLogout(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return map[string]bool{"ok": true}, s.svc.Logout(r.Context(), bearer(r))
}

func (s *Server) operatorSlots(w http.ResponseWriter, r *http.Request, m core.Machine, p core.Principal) (any, error) {
	return s.svc.OperatorSlots(r.Context(), m)
}

func (s *Server) saveRestock(w http.ResponseWriter, r *http.Request, m core.Machine, p core.Principal) (any, error) {
	var in core.RestockInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.SaveRestock(r.Context(), m, p, in)
}
