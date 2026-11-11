package http

import (
	"net/http"

	"vending/internal/model"
)

// The products an admin manages (UC-07). A product save carries its picture as
// a data URL, so these are the only endpoints with the larger body limit.

func (s *Server) products(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	list, err := s.uc.Product.List(r.Context(), newQuery(r).bool("includeArchived"))
	if err != nil {
		return nil, err
	}
	return productViews(list), nil
}

func (s *Server) createProduct(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	var b productBody
	if err := decode(w, r, &b, imageBodyLimit); err != nil {
		return nil, err
	}
	d, err := s.uc.Product.Create(r.Context(), p, b.input())
	if err != nil {
		return nil, err
	}
	return created{productView(d)}, nil
}

func (s *Server) updateProduct(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	var b productBody
	if err := decode(w, r, &b, imageBodyLimit); err != nil {
		return nil, err
	}
	d, err := s.uc.Product.Update(r.Context(), p, id, b.input())
	if err != nil {
		return nil, err
	}
	return productView(d), nil
}

func (s *Server) archiveProduct(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	d, err := s.uc.Product.Archive(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	return productView(d), nil
}

func (s *Server) restoreProduct(w http.ResponseWriter, r *http.Request, p model.Principal) (any, error) {
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	d, err := s.uc.Product.Restore(r.Context(), p, id)
	if err != nil {
		return nil, err
	}
	return productView(d), nil
}
