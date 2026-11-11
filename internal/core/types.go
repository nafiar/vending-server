package core

import (
	"vending/internal/model"
	"vending/internal/repository/mysqldb"
)

// The aliases the remaining core stories still use. They are model types; this
// file exists only so that core's files need not be rewritten twice, once to
// move them out of core and again to rename what they call things. It goes with
// the last of core (docs/REARCHITECTURE.md phase 8).

// Principal is a logged-in staff member, taken from a session token.
type Principal = model.Principal

// Machine is the Kiosk Agent of one machine, taken from its machine key.
type Machine = model.Machine

const (
	roleAdmin    = model.RoleAdmin
	roleOperator = model.RoleOperator
)

// Short names for the two repository helpers core's remaining SQL uses.
type (
	queryer    = mysqldb.Queryer
	rowScanner = mysqldb.RowScanner
)
