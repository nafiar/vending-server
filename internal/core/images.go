package core

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maxImageBytes = 2 << 20 // 2 MB (FR-20)
	maxImageSide  = 6000    // pixels; refuses decompression bombs before anything decodes them
	productImages = "products"
)

// imagePrefixes are the data URLs a browser's FileReader produces for JPG and PNG.
var imagePrefixes = map[string]string{
	"data:image/png;base64,":  ".png",
	"data:image/jpeg;base64,": ".jpg",
	"data:image/jpg;base64,":  ".jpg",
}

// decodeImage checks an uploaded data URL: JPG or PNG by content (not just by
// name), at most 2 MB, with sane dimensions. It returns the bytes and extension.
func decodeImage(dataURL string) ([]byte, string, string) {
	var ext, payload string
	for prefix, e := range imagePrefixes {
		if strings.HasPrefix(dataURL, prefix) {
			ext, payload = e, dataURL[len(prefix):]
			break
		}
	}
	if ext == "" {
		return nil, "", "Gambar harus JPG atau PNG."
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > maxImageBytes+3 {
		return nil, "", "Ukuran gambar paling besar 2 MB."
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", "Gambar tidak bisa dibaca."
	}
	if len(data) > maxImageBytes {
		return nil, "", "Ukuran gambar paling besar 2 MB."
	}
	var cfg image.Config
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
		ext = ".png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
		ext = ".jpg"
	default:
		return nil, "", "Gambar harus JPG atau PNG."
	}
	if err != nil {
		return nil, "", "Gambar tidak bisa dibaca."
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageSide || cfg.Height > maxImageSide {
		return nil, "", fmt.Sprintf("Gambar paling besar %d × %d piksel.", maxImageSide, maxImageSide)
	}
	return data, ext, ""
}

// storeImage writes the bytes under a random name and returns the public URL.
// Names are never reused, so the files can be cached forever.
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

var placeholderColors = []string{"#7b3fa0", "#1f7a8c", "#c0392b", "#2e8b57", "#d35400", "#34495e", "#8e44ad", "#b07d12", "#5d6d7e"}

// placeholderSVG draws a 3:4 tile with the product's initials, used when a
// product is saved without an image so the kiosk never shows a broken picture.
func placeholderSVG(name string, seed int) []byte {
	var initials []rune
	for _, w := range strings.Fields(name) {
		r := []rune(w)
		if len(r) > 0 && (unicode.IsLetter(r[0]) || unicode.IsDigit(r[0])) {
			initials = append(initials, unicode.ToUpper(r[0]))
		}
		if len(initials) == 2 {
			break
		}
	}
	if len(initials) == 0 {
		initials = []rune{'?'}
	}
	color := placeholderColors[seed%len(placeholderColors)]
	return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 400">` +
		`<rect width="300" height="400" rx="18" fill="` + color + `"/>` +
		`<rect x="18" y="18" width="264" height="364" rx="12" fill="none" stroke="#fff" stroke-opacity=".35" stroke-width="4"/>` +
		`<circle cx="150" cy="190" r="80" fill="#fff" fill-opacity=".16"/>` +
		`<text x="150" y="218" font-family="Segoe UI, Arial, sans-serif" font-size="80" font-weight="700" fill="#fff" text-anchor="middle">` +
		html.EscapeString(string(initials)) + `</text></svg>`)
}

// StoreImage saves image bytes under a new random name and returns the URL.
// The demo seed uses it for the product tiles.
func (s *Service) StoreImage(data []byte, ext string) (string, error) { return s.storeImage(data, ext) }

// PlaceholderSVG draws the tile used for a product without an image.
func PlaceholderSVG(name string, seed int) []byte { return placeholderSVG(name, seed) }
