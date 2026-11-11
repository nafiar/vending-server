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

// User is a staff account: an admin who uses the web panel, or an operator who
// signs in on a machine to restock it.
//
// The lockout fields are part of the account rather than of a login attempt,
// because the rule they serve is about the account: five wrong passwords shut
// it for five minutes, and during that time even the right password is refused
// (FR-13).
type User struct {
	ID           int64
	Name         string
	Username     string
	PasswordHash string
	Role         string
	IsActive     bool
	FailedLogins int
	// LockedUntil is when the account reopens, nil when it is not shut.
	LockedUntil *time.Time
	CreatedAt   time.Time
}

// IsLockedAt reports whether the account is shut at this moment, and for how
// much longer.
func (u User) IsLockedAt(now time.Time) (time.Duration, bool) {
	if u.LockedUntil == nil || !u.LockedUntil.After(now) {
		return 0, false
	}
	return u.LockedUntil.Sub(now), true
}

// Login is a successful sign-in: the token the client sends from now on, and
// who it belongs to, for the greeting on the screen.
type Login struct {
	Token    string
	Name     string
	Username string
}

// Session is one signed-in session. The token itself is never stored, only its
// hash, so a stolen database row cannot be used as a token.
type Session struct {
	ID         int64
	UserID     int64
	Kind       string
	TokenHash  string
	MachineID  *int64 // operator sessions only: the machine signed in on
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// AuthSession is what looking up a token yields: the principal the request
// would act as, plus the two facts that decide whether the token is still good.
// They are read together because they are decided together: an idle session and
// a deactivated account both end a session, and one query answers both.
type AuthSession struct {
	Principal  Principal
	LastSeenAt time.Time
	UserActive bool
}

// Account limits. The password bounds differ by role on purpose: an admin's
// password protects every machine's figures, an operator's protects one visit
// and is typed on a kiosk by someone standing up.
const (
	MaxUserNameRunes    = 100
	MinOperatorPassword = 6
	MinAdminPassword    = 8
	MaxPassword         = 128
)

// OperatorDetail is a staff account as the admin's list shows it: the account,
// how often they have been out to a machine lately, and where they were last.
type OperatorDetail struct {
	User User
	// Visits30 is how many restock visits they have made in the last 30 days.
	Visits30 int64
	// Last is their most recent visit, nil if they have never made one.
	Last *Visit
}
