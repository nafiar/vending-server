package core

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-sql-driver/mysql"
)

// Error is a failure the API reports to the caller as JSON:
// {"code": ..., "message": ..., "fields": {...}, ...Extra}.
// Codes match what the admin web and the kiosk already handle.
// Messages are in Bahasa Indonesia because the screens show them as they are.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string // VALIDATION and INVALID_INPUT: per-field messages
	Extra   map[string]any    // merged into the JSON body, e.g. attemptsLeft, catalog
}

func (e *Error) Error() string { return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message) }

// AsError returns the *Error inside err, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func errNotFound(msg string) *Error     { return newErr(http.StatusNotFound, "NOT_FOUND", msg) }
func errUnauthorized(msg string) *Error { return newErr(http.StatusUnauthorized, "UNAUTHORIZED", msg) }
func errForbidden(msg string) *Error    { return newErr(http.StatusForbidden, "FORBIDDEN", msg) }

func errConflict(code, msg string, extra map[string]any) *Error {
	e := newErr(http.StatusConflict, code, msg)
	e.Extra = extra
	return e
}

// ErrBadRequest is for a body or query that can't be read at all.
func ErrBadRequest(msg string) *Error { return newErr(http.StatusBadRequest, "BAD_REQUEST", msg) }

// fieldErrors collects validation messages per field.
type fieldErrors map[string]string

func (f fieldErrors) add(field, msg string) {
	if _, dup := f[field]; !dup {
		f[field] = msg
	}
}

// err returns the admin-web validation error, or nil when there is nothing to report.
func (f fieldErrors) err() error {
	if len(f) == 0 {
		return nil
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: "VALIDATION", Message: "Periksa isian yang ditandai.", Fields: f}
}

// invalidInput is the kiosk-side equivalent of a validation error.
func invalidInput(msg string, fields map[string]string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "INVALID_INPUT", Message: msg, Fields: fields}
}

// MySQL error numbers the service reacts to.
const (
	mysqlDuplicateKey = 1062
	mysqlLockWait     = 1205
	mysqlDeadlock     = 1213
)

func isMySQLError(err error, number uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == number
}
