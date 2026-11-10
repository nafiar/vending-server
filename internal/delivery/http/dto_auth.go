package http

import "vending/internal/model"

// The wire shapes for signing in and for "who am I", and the mappers from the
// domain. Field names here are the contract the admin web (admin/README.md) and
// the kiosk's operator screen read; testdata/contract.golden records them.

// loginBody is the body of both login endpoints.
type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginDTO is a successful sign-in. The token is the only copy the client will
// ever get: the server keeps a hash of it and cannot show it again.
type loginDTO struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

func loginView(l model.Login) loginDTO {
	return loginDTO{Token: l.Token, Name: l.Name, Username: l.Username}
}

// meDTO is the signed-in admin, as the panel's header shows them. demoMode is a
// property of this server rather than of the person, and it is reported here
// because this is the first call the panel makes.
type meDTO struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Role     string `json:"role"`
	DemoMode bool   `json:"demoMode"`
}

func meView(p model.Principal, demoMode bool) meDTO {
	return meDTO{Name: p.Name, Username: p.Username, Role: p.Role, DemoMode: demoMode}
}

// okDTO is the answer to a request that has nothing to report but success,
// such as signing out.
type okDTO struct {
	OK bool `json:"ok"`
}

func ok() okDTO { return okDTO{OK: true} }
