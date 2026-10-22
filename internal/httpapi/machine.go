package httpapi

import (
	"net/http"

	"vending/internal/core"
)

// Machine endpoints, called by the Kiosk Agent with its machine key in the
// X-Machine-Key header. The agent forwards the kiosk page's calls
// (kiosk/README.md) to these paths, and reports dispense results itself.

func (s *Server) catalog(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return s.svc.GetCatalog(r.Context(), m)
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return s.svc.Heartbeat(r.Context(), m)
}

func (s *Server) createTransaction(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var in core.CreateTransactionInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.CreateTransaction(r.Context(), m, in)
}

func (s *Server) transactionStatus(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return s.svc.TransactionStatus(r.Context(), m, r.PathValue("id"))
}

func (s *Server) pay(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return s.svc.Pay(r.Context(), m, r.PathValue("id"))
}

func (s *Server) result(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var in core.ResultInput
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.RecordResult(r.Context(), m, r.PathValue("id"), in)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	var in struct {
		Reason string `json:"reason"` // CANCEL (customer tapped Batal) or TIMEOUT (60 s passed)
	}
	if err := decode(w, r, &in, defaultBodyLimit); err != nil {
		return nil, err
	}
	return s.svc.Cancel(r.Context(), m, r.PathValue("id"), in.Reason)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request, m core.Machine) (any, error) {
	return s.svc.Report(r.Context(), m, r.PathValue("id"))
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
