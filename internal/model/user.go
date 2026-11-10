package model

import "time"

// Roles a staff account can have (the users.role ENUM).
const (
	RoleAdmin    = "ADMIN"
	RoleOperator = "OPERATOR"
)

// Session kinds (the sessions.kind ENUM). An admin session belongs to the admin
// web; an operator session belongs to one machine and expires far sooner (BR-13).
const (
	KindAdmin    = "ADMIN"
	KindOperator = "OPERATOR"
)

// Principal is a logged-in staff member, taken from a session token.
type Principal struct {
	SessionID int64
	UserID    int64
	Name      string
	Username  string
	Role      string
	MachineID int64     // operator sessions only: the machine the operator logged in on
	StartedAt time.Time // when the session began; for an operator, the start of the visit
}
