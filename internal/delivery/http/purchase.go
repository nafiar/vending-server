package http

import (
	"net/http"

	"vending/internal/model"
	"vending/internal/usecase/purchase"
)

// One customer's purchase (UC-02, UC-03). The kiosk page calls these through
// the agent, which adds the machine key.

func (s *Server) createTransaction(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
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

func (s *Server) transactionStatus(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	return state(s.uc.Purchase.Status(r.Context(), m, r.PathValue("id")))
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	return state(s.uc.Purchase.Pay(r.Context(), m, r.PathValue("id")))
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	var b resultBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	return state(s.uc.Purchase.Result(r.Context(), m, r.PathValue("id"),
		purchase.ResultInput{Status: b.Status, FailureReason: b.FailureReason}))
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
	var b cancelBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	return state(s.uc.Purchase.Cancel(r.Context(), m, r.PathValue("id"), b.Reason))
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, m model.Machine) (any, error) {
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
