package http

import (
	"vending/internal/model"
	"vending/internal/usecase/operator"
)

// The wire shapes for the staff accounts an admin manages, and the mappers from
// the domain. Field names here are the contract the admin web reads
// (admin/README.md); testdata/contract.golden records them.

// operatorBody is the body of POST /api/admin/operators.
type operatorBody struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (b operatorBody) input() operator.Input {
	return operator.Input{Name: b.Name, Username: b.Username, Password: b.Password}
}

// operatorDTO is a staff account as the admin's list shows it. The password
// hash and the lockout state are not here: nothing on this screen needs them.
type operatorDTO struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Username         string  `json:"username"`
	Role             string  `json:"role"`
	IsActive         bool    `json:"isActive"`
	CreatedAt        int64   `json:"createdAt"`
	LastVisitAt      *int64  `json:"lastVisitAt"`
	LastVisitMachine *string `json:"lastVisitMachine"`
	Visits30         int64   `json:"visits30"`
}

func operatorView(d model.OperatorDetail) operatorDTO {
	u := d.User
	out := operatorDTO{
		ID:        u.ID,
		Name:      u.Name,
		Username:  u.Username,
		Role:      u.Role,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt.UnixMilli(),
		Visits30:  d.Visits30,
	}
	if d.Last != nil {
		at := d.Last.At.UnixMilli()
		code := d.Last.MachineCode
		out.LastVisitAt, out.LastVisitMachine = &at, &code
	}
	return out
}

func operatorViews(list []model.OperatorDetail) []operatorDTO {
	out := make([]operatorDTO, 0, len(list))
	for _, d := range list {
		out = append(out, operatorView(d))
	}
	return out
}

// createdIDDTO is the answer to a create that has nothing to report but the id
// of what was made.
type createdIDDTO struct {
	ID int64 `json:"id"`
}

// temporaryPasswordDTO carries a reset password, which the server will never be
// able to show again: only its hash is kept.
type temporaryPasswordDTO struct {
	TemporaryPassword string `json:"temporaryPassword"`
}
