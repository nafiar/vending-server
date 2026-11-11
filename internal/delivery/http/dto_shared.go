package http

// The small wire shapes more than one story sends: a thing's id and name, the
// machine a row happened at, the member of staff who did it. They live here
// rather than in one story's file because the admin web reads the same three
// shapes on several screens, and two copies of a field name is how a contract
// starts to drift.
//
// Every time on the wire is Unix milliseconds in UTC, which the browser turns
// into WIB.

// idNameDTO identifies something by id and says what it is called.
type idNameDTO struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// machineRefDTO is enough of a machine to show a row without a second lookup:
// the code an operator reads off the cabinet, and the name an admin gave it.
type machineRefDTO struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// userRefDTO is enough of a staff member to show who did something. It carries
// the username as well as the name, because two people can share a name and the
// change log has to say which one it was.
type userRefDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}
