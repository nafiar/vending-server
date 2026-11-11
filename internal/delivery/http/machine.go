package http

import (
	"net/http"
	"strconv"

	"vending/internal/model"
)

// The machines an admin registers, and the slots in them (UC-08).

func (s *Server) machines(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	list, err := s.uc.Machine.List(r.Context())
	if err != nil {
		return nil, err
	}
	return machineViews(list), nil
}

func (s *Server) machine(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	d, err := s.uc.Machine.Get(r.Context(), id)
	if err != nil {
		return nil, err
	}
	return machineView(d), nil
}

func (s *Server) createMachine(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	var b machineBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	k, err := s.uc.Machine.Create(r.Context(), p, b.input())
	if err != nil {
		return nil, err
	}
	return created{machineWithKeyView(k)}, nil
}

func (s *Server) setMachineActive(active bool) adminFunc {
	return func(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
		id, err := pathID(r, "id")
		if err != nil {
			return nil, err
		}
		d, err := s.uc.Machine.SetActive(r.Context(), p, id, active)
		if err != nil {
			return nil, err
		}
		return machineView(d), nil
	}
}

func (s *Server) rotateMachineKey(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	k, err := s.uc.Machine.RotateKey(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	return machineWithKeyView(k), nil
}

func (s *Server) updateSlot(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	// The slot number is part of the path, so a value that is not one is a
	// path that names no slot.
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 1 {
		return nil, model.NotFound("Slot tidak ditemukan.")
	}
	var b slotBody
	if err := decode(w, r, &b, defaultBodyLimit); err != nil {
		return nil, err
	}
	sl, err := s.uc.Machine.UpdateSlot(r.Context(), p, id, n, b.input())
	if err != nil {
		return nil, err
	}
	return slotView(sl), nil
}
