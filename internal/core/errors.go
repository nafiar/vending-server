package core

import (
	"vending/internal/model"
)

// The business error and its codes live in internal/model now, and the delivery
// layer maps a code to an HTTP status. These aliases and wrappers keep the rest
// of core unchanged while the layers are pulled apart.

// Error is a failure the API reports to the caller as JSON.
type Error = model.Error

// AsError returns the *Error inside err, if any.
func AsError(err error) (*Error, bool) { return model.AsError(err) }

// ErrBadRequest is for a body or query that can't be read at all.
func ErrBadRequest(msg string) *Error { return model.BadRequest(msg) }

func errNotFound(msg string) *Error     { return model.NotFound(msg) }
func errUnauthorized(msg string) *Error { return model.Unauthorized(msg) }
func errForbidden(msg string) *Error    { return model.Forbidden(msg) }

func errConflict(code, msg string, extra map[string]any) *Error {
	return model.Conflict(code, msg, extra)
}

// invalidInput is the kiosk-side equivalent of a validation error.
func invalidInput(msg string, fields map[string]string) *Error {
	return model.InvalidInput(msg, fields)
}

// fieldErrors collects validation messages per field.
type fieldErrors = model.FieldErrors
