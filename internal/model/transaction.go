package model

// Transaction states (PRD, "Purchase sequence and transaction states"). A final
// state never changes (BR-11).
const (
	StatusPending   = "PENDING"
	StatusPaid      = "PAID"
	StatusDispensed = "DISPENSED"
	StatusFailed    = "FAILED"
	StatusCancelled = "CANCELLED"
	StatusExpired   = "EXPIRED"
)

// IsFinal reports whether a transaction in this state can still change.
func IsFinal(status string) bool {
	switch status {
	case StatusDispensed, StatusFailed, StatusCancelled, StatusExpired:
		return true
	}
	return false
}
