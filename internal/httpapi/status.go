package httpapi

import (
	"net/http"

	"vending/internal/model"
)

// statusFor maps a business error code to its HTTP status. This is the only
// place in the system where an error becomes a status: the rules below delivery
// raise a *model.Error with a code and a message, and never a status.
//
// The admin web and the kiosk branch on both the status and the code, so this
// table is part of the API contract. TestStatusForCoversEveryCode keeps it
// complete, and testdata/contract.golden records what each route answers.
var statusByCode = map[string]int{
	model.CodeBadRequest:        http.StatusBadRequest,            // 400
	model.CodeUnauthorized:      http.StatusUnauthorized,          // 401
	model.CodeInvalidLogin:      http.StatusUnauthorized,          // 401
	model.CodeInvalidMachineKey: http.StatusUnauthorized,          // 401
	model.CodeForbidden:         http.StatusForbidden,             // 403
	model.CodeMachineInactive:   http.StatusForbidden,             // 403
	model.CodeNotFound:          http.StatusNotFound,              // 404
	model.CodeExpired:           http.StatusGone,                  // 410: the payment window closed (BR-06)
	model.CodePayloadTooLarge:   http.StatusRequestEntityTooLarge, // 413
	model.CodeLocked:            http.StatusLocked,                // 423: too many wrong passwords (FR-13)
	model.CodeValidation:        http.StatusUnprocessableEntity,   // 422: the admin web's form errors
	model.CodeInvalidInput:      http.StatusUnprocessableEntity,   // 422: the kiosk's equivalent
	model.CodeInternal:          http.StatusInternalServerError,   // 500

	// The 409 family: the request clashes with the current state.
	model.CodeConflict:          http.StatusConflict,
	model.CodeSoldOut:           http.StatusConflict,
	model.CodeCatalogStale:      http.StatusConflict,
	model.CodeTransactionClosed: http.StatusConflict,
	model.CodeNotPaid:           http.StatusConflict,
	model.CodeNotReportable:     http.StatusConflict,
	model.CodeReportWindowShut:  http.StatusConflict,
}

// statusFor returns the status for a code. An unmapped code is a mistake in the
// rules, not in the request, so it is reported as a server fault rather than as
// a misleading 200.
func statusFor(code string) int {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}
