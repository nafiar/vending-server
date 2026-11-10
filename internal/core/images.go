package core

import "vending/internal/common/imagex"

// storeImage saves the bytes through the image repository and returns the URL.
func (s *Service) storeImage(data []byte, ext string) (string, error) {
	return s.images.Save(data, ext)
}

// StoreImage saves image bytes under a new name and returns the URL.
// The demo seed uses it for the product tiles.
func (s *Service) StoreImage(data []byte, ext string) (string, error) { return s.storeImage(data, ext) }

// PlaceholderSVG draws the tile used for a product without an image.
func PlaceholderSVG(name string, seed int) []byte { return imagex.Placeholder(name, seed) }
