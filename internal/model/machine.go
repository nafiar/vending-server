package model

// Machine is the Kiosk Agent of one machine, taken from its machine key.
type Machine struct {
	ID   int64
	Code string
	Name string
}
