package http

// Writing the answer: the JSON body, and the one place a business error becomes
// an HTTP status and a domain value becomes JSON field names.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"vending/internal/model"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError turns err into the JSON error body the screens understand:
// {"code", "message", "fields"?, ...extra}. Anything that is not a *model.Error
// is a server fault: it is logged with the request id and reported as INTERNAL.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := model.AsError(err); ok {
		body := map[string]any{"code": e.Code, "message": e.Message}
		if len(e.Fields) > 0 {
			body["fields"] = e.Fields
		}
		for k, v := range e.Extra {
			body[k] = wireValue(v)
		}
		writeJSON(w, statusFor(e.Code), body)
		return
	}
	rid := requestIDFrom(r.Context())
	if errors.Is(err, context.Canceled) {
		s.log.Debug("request cancelled by the client", "request_id", rid)
		return
	}
	s.log.Error("request failed", "err", err, "method", r.Method, "path", r.URL.Path, "request_id", rid)
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"code":      "INTERNAL",
		"message":   "Terjadi kesalahan di server. Coba lagi.",
		"requestId": rid,
	})
}

// wireValue maps a domain value carried in an error's Extra to its wire shape.
// CATALOG_STALE answers with the fresh catalog and a refused second dispense
// result answers with the state it already has; a model type has no JSON field
// names of its own, because those belong to this layer.
func wireValue(v any) any {
	switch x := v.(type) {
	case model.Catalog:
		return catalogView(x)
	case model.Transaction:
		return transactionStateView(x)
	default:
		return v
	}
}
