// Package local stores product images as files under the upload directory,
// which the server also serves at /uploads/.
package local

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"vending/internal/repository/imagestore"
)

// products is the subdirectory, and the first segment of the public URL.
const products = "products"

// Local writes images to a directory. It is a singleton: one is built in
// cmd/vending-server and shared.
type Local struct{ dir string }

// Compile-time proof that the driver still matches the port.
var _ imagestore.Repository = (*Local)(nil)

// New returns the driver over dir (the UPLOAD_DIR setting).
func New(dir string) *Local { return &Local{dir: dir} }

// Save writes the bytes under a random name and returns the public URL. The
// name is 12 random bytes, so it is never reused and the file can be cached
// forever.
func (l *Local) Save(data []byte, ext string) (string, error) {
	dir := filepath.Join(l.dir, products)
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
	return "/uploads/" + products + "/" + name, nil
}
