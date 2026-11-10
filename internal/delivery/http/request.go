package http

// Reading the request: the body, the bearer token, the path values and the
// query string. Everything here refuses what it cannot use rather than reading
// it as a zero, because a filter silently read as 0 shows an admin the wrong
// figures and tells them nothing.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"vending/internal/model"
)

const (
	defaultBodyLimit = 1 << 20 // 1 MB
	imageBodyLimit   = 6 << 20 // product saves carry the image as base64 (2 MB file ≈ 2.7 MB of text)
)

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
		return model.NewError(model.CodePayloadTooLarge, "Data terlalu besar. Gambar paling besar 2 MB.")
	}
	return model.BadRequest("Body harus JSON yang valid: " + err.Error())
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
		return 0, model.NotFound("Data tidak ditemukan.")
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
	return &model.Error{Code: model.CodeValidation, Message: "Periksa filter yang dipakai.", Fields: q.fields}
}

// logLevelFor keeps static files out of the info log.
func logLevelFor(path string) slog.Level {
	if strings.HasPrefix(path, "/api/") {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// intField reads a whole number sent as JSON. The admin web and the kiosk send
// these as numbers, but a browser can turn a large one into 1e9 and a careless
// client sends "3"; json.Number holds the text either way, so both are refused
// here rather than silently read as zero.
func intField(n json.Number) (int64, bool) {
	s := strings.TrimSpace(n.String())
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}
