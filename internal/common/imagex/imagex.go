// Package imagex checks an uploaded product image and draws the stand-in tile
// for a product saved without one. It is pure: nothing here touches the disk,
// so the rules can validate an upload before any repository stores it.
package imagex

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"unicode"
)

const (
	MaxBytes = 2 << 20 // 2 MB (FR-20)
	maxSide  = 6000    // pixels; refuses decompression bombs before anything decodes them
)

// prefixes are the data URLs a browser's FileReader produces for JPG and PNG.
var prefixes = map[string]string{
	"data:image/png;base64,":  ".png",
	"data:image/jpeg;base64,": ".jpg",
	"data:image/jpg;base64,":  ".jpg",
}

// Decode checks an uploaded data URL: JPG or PNG by content (not just by name),
// at most 2 MB, with sane dimensions. It returns the bytes and the extension,
// or a message for the form's "image" field explaining what is wrong.
func Decode(dataURL string) (data []byte, ext string, problem string) {
	var payload string
	for prefix, e := range prefixes {
		if strings.HasPrefix(dataURL, prefix) {
			ext, payload = e, dataURL[len(prefix):]
			break
		}
	}
	if ext == "" {
		return nil, "", "Gambar harus JPG atau PNG."
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > MaxBytes+3 {
		return nil, "", "Ukuran gambar paling besar 2 MB."
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", "Gambar tidak bisa dibaca."
	}
	if len(data) > MaxBytes {
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
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxSide || cfg.Height > maxSide {
		return nil, "", fmt.Sprintf("Gambar paling besar %d × %d piksel.", maxSide, maxSide)
	}
	return data, ext, ""
}

var placeholderColors = []string{"#7b3fa0", "#1f7a8c", "#c0392b", "#2e8b57", "#d35400", "#34495e", "#8e44ad", "#b07d12", "#5d6d7e"}

// Placeholder draws a 3:4 tile with the product's initials, used when a product
// is saved without an image so the kiosk never shows a broken picture. The name
// is escaped, so a product called "<script>" cannot reach the page as markup.
func Placeholder(name string, seed int) []byte {
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
