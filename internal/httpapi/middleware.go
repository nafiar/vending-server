package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 1

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func requestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// statusWriter remembers the status and size for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.status, w.wrote = http.StatusOK, true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// adminCSP allows only this server's own scripts and styles, plus the Google
// Fonts stylesheet the admin page uses (it falls back to system fonts offline).
// Inline styles stay allowed: the screens set widths with style attributes.
const adminCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' https://fonts.gstatic.com; img-src 'self' data: blob:; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// withMiddleware adds, around every request: a request id, security headers,
// panic recovery and one access-log line.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(rid) {
			rid = newRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey, rid)
		r = r.WithContext(ctx)

		h := w.Header()
		h.Set("X-Request-ID", rid)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			h.Set("Cache-Control", "no-store")
		case strings.HasPrefix(r.URL.Path, "/uploads/"):
			// Uploaded files are images only; never let one run as a page.
			h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
		default:
			h.Set("Content-Security-Policy", adminCSP)
		}

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic while serving a request", "panic", rec, "path", r.URL.Path, "request_id", rid, "stack", string(debug.Stack()))
				if !sw.wrote {
					writeJSON(sw, http.StatusInternalServerError, map[string]any{
						"code": "INTERNAL", "message": "Terjadi kesalahan di server. Coba lagi.", "requestId": rid,
					})
				}
			}
			s.log.Log(ctx, logLevelFor(r.URL.Path), "request",
				"method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "bytes", sw.bytes, "request_id", rid)
		}()
		next.ServeHTTP(sw, r)
	})
}
