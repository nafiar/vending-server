package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"vending/internal/core"
)

const (
	defaultBodyLimit = 1 << 20 // 1 MB
	imageBodyLimit   = 6 << 20 // product saves carry the image as base64 (2 MB file ≈ 2.7 MB of text)
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError turns err into the JSON error body the screens understand:
// {"code", "message", "fields"?, ...extra}. Anything that is not a *core.Error
// is a server fault: it is logged with the request id and reported as INTERNAL.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := core.AsError(err); ok {
		body := map[string]any{"code": e.Code, "message": e.Message}
		if len(e.Fields) > 0 {
			body["fields"] = e.Fields
		}
		for k, v := range e.Extra {
			body[k] = v
		}
		writeJSON(w, e.Status, body)
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

// decode reads a JSON body into dst. An empty body is allowed and leaves dst
// as it is, so POST endpoints without parameters accept {} or nothing.
func decode(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if r.Body == nil {
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return &core.Error{Status: http.StatusRequestEntityTooLarge, Code: "PAYLOAD_TOO_LARGE",
			Message: "Data terlalu besar. Gambar paling besar 2 MB."}
	}
	return core.ErrBadRequest("Body harus JSON yang valid: " + err.Error())
}

// bearer reads "Authorization: Bearer <token>".
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// pathID reads a positive integer path value such as {id}.
func pathID(r *http.Request, name string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v <= 0 {
		return 0, &core.Error{Status: http.StatusNotFound, Code: "NOT_FOUND", Message: "Data tidak ditemukan."}
	}
	return v, nil
}

// query helpers: a bad number is a validation error on that field.
type query struct {
	r      *http.Request
	fields map[string]string
}

func newQuery(r *http.Request) *query { return &query{r: r, fields: map[string]string{}} }

func (q *query) str(name string) string { return strings.TrimSpace(q.r.URL.Query().Get(name)) }

func (q *query) int(name string) int64 {
	v := q.str(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		q.fields[name] = "Harus berupa angka."
		return 0
	}
	return n
}

func (q *query) bool(name string) bool {
	switch strings.ToLower(q.str(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (q *query) err() error {
	if len(q.fields) == 0 {
		return nil
	}
	return &core.Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION", Message: "Periksa filter yang dipakai.", Fields: q.fields}
}

// logLevelFor keeps static files out of the info log.
func logLevelFor(path string) slog.Level {
	if strings.HasPrefix(path, "/api/") {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}
