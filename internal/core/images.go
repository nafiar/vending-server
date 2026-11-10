package core

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"vending/internal/common/imagex"
)

const productImages = "products"

// storeImage writes the bytes under a random name and returns the public URL.
// Names are never reused, so the files can be cached forever.
//
// This is the one piece of core that touches the disk; it becomes
// repository/imagestore (see docs/REARCHITECTURE.md).
func (s *Service) storeImage(data []byte, ext string) (string, error) {
	dir := filepath.Join(s.uploadDir, productImages)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	var b [12]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	name := hex.EncodeToString(b[:]) + ext
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return "", err
	}
	return "/uploads/" + productImages + "/" + name, nil
}

// StoreImage saves image bytes under a new random name and returns the URL.
// The demo seed uses it for the product tiles.
func (s *Service) StoreImage(data []byte, ext string) (string, error) { return s.storeImage(data, ext) }

// PlaceholderSVG draws the tile used for a product without an image.
func PlaceholderSVG(name string, seed int) []byte { return imagex.Placeholder(name, seed) }
