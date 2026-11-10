// Package model holds the domain types of the vending information system:
// plain data, the constants the rules compare against, and the business error.
// It imports nothing from internal/, so every layer can share these types
// without importing each other.
package model

import (
	"errors"
	"fmt"
)

// Error codes. The API reports one of these to the caller, and the delivery
// layer maps each to an HTTP status in one place (delivery/http/status.go), so
// no rule below delivery decides a status and nothing matches on error strings.
// The admin web and the kiosk branch on these strings, so they are a contract.
const (
	CodeBadRequest        = "BAD_REQUEST"
	CodeUnauthorized      = "UNAUTHORIZED"
	CodeInvalidLogin      = "INVALID_LOGIN"
	CodeInvalidMachineKey = "INVALID_MACHINE_KEY"
	CodeForbidden         = "FORBIDDEN"
	CodeMachineInactive   = "MACHINE_INACTIVE"
	CodeNotFound          = "NOT_FOUND"
	CodeExpired           = "EXPIRED"
	CodePayloadTooLarge   = "PAYLOAD_TOO_LARGE"
	CodeLocked            = "LOCKED"
	CodeValidation        = "VALIDATION"
	CodeInvalidInput      = "INVALID_INPUT"
	CodeConflict          = "CONFLICT"
	CodeSoldOut           = "SOLD_OUT"
	CodeCatalogStale      = "CATALOG_STALE"
	CodeTransactionClosed = "TRANSACTION_CLOSED"
	CodeNotPaid           = "NOT_PAID"
	CodeNotReportable     = "NOT_REPORTABLE"
	CodeReportWindowShut  = "REPORT_WINDOW_CLOSED"
	CodeInternal          = "INTERNAL"
)

// ErrNoRecord is what a repository returns when the row asked for is not
// there. A usecase turns it into the message its story needs ("Transaksi tidak
// ditemukan.", "Slot tidak ditemukan."), so the wording stays with the rule and
// no repository writes screen text.
var ErrNoRecord = errors.New("no such record")

// ErrDuplicateID is what a repository returns when the row it was asked to
// create already exists under that id. The kiosk generates the transaction id,
// so two retries of one request can race; the usecase answers by re-reading the
// transaction that won rather than by failing (NFR-03).
var ErrDuplicateID = errors.New("that id is already used")

// Error is a failure the API reports as JSON: {"code", "message", "fields"?,
// ...Extra}. Messages are in Bahasa Indonesia because the screens show them as
// they are. An error that is not an *Error is a server fault, reported as
// CodeInternal and logged with the request id.
type Error struct {
	Code    string
	Message string
	Fields  map[string]string // VALIDATION and INVALID_INPUT: one message per field
	Extra   map[string]any    // merged into the JSON body, e.g. attemptsLeft, catalog
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// AsError returns the *Error inside err, if any.
func AsError(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// NewError builds an error with a code and a message for the screen.
func NewError(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// The failures the rules raise, one constructor each.

func NotFound(msg string) *Error        { return NewError(CodeNotFound, msg) }
func Unauthorized(msg string) *Error    { return NewError(CodeUnauthorized, msg) }
func Forbidden(msg string) *Error       { return NewError(CodeForbidden, msg) }
func BadRequest(msg string) *Error      { return NewError(CodeBadRequest, msg) }
func MachineInactive(msg string) *Error { return NewError(CodeMachineInactive, msg) }
func Expired(msg string) *Error         { return NewError(CodeExpired, msg) }

// Conflict is a request that clashes with the current state: SOLD_OUT,
// CATALOG_STALE, NOT_PAID and the rest of the 409 family.
func Conflict(code, msg string, extra map[string]any) *Error {
	return &Error{Code: code, Message: msg, Extra: extra}
}

// InvalidInput is the kiosk-side validation failure (the agent sent something
// the rules cannot use).
func InvalidInput(msg string, fields map[string]string) *Error {
	return &Error{Code: CodeInvalidInput, Message: msg, Fields: fields}
}

// Locked is an account shut after too many wrong passwords (FR-13).
func Locked(msg string, extra map[string]any) *Error {
	return &Error{Code: CodeLocked, Message: msg, Extra: extra}
}

// FieldErrors collects one validation message per field, for the admin web's
// forms. The first message for a field wins, so a field is reported once.
type FieldErrors map[string]string

func (f FieldErrors) Add(field, msg string) {
	if _, dup := f[field]; !dup {
		f[field] = msg
	}
}

// Err returns the VALIDATION error, or nil when there is nothing to report.
func (f FieldErrors) Err() error {
	if len(f) == 0 {
		return nil
	}
	return &Error{Code: CodeValidation, Message: "Periksa isian yang ditandai.", Fields: f}
}

// Codes is every error code the rules can raise. The delivery layer's status
// table is checked against this list, so a new code must be added here too or
// the build fails rather than silently answering 500.
var Codes = []string{
	CodeBadRequest, CodeUnauthorized, CodeInvalidLogin, CodeInvalidMachineKey,
	CodeForbidden, CodeMachineInactive, CodeNotFound, CodeExpired,
	CodePayloadTooLarge, CodeLocked, CodeValidation, CodeInvalidInput,
	CodeConflict, CodeSoldOut, CodeCatalogStale, CodeTransactionClosed,
	CodeNotPaid, CodeNotReportable, CodeReportWindowShut, CodeInternal,
}
