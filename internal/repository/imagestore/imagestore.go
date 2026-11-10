// Package imagestore keeps the product images an admin uploads. The port is
// here; the drivers live in subpackages (local writes them to disk).
package imagestore

//go:generate mockgen -source=imagestore.go -destination=mock/imagestore_mock.go -package=mock

// Repository stores one image and returns the URL the screens load it from.
//
// An implementation must never reuse a name: the URL it returns is cached for a
// year, so a new image has to be a new URL. ext is ".png", ".jpg" or ".svg",
// already checked by common/imagex.
type Repository interface {
	Save(data []byte, ext string) (url string, err error)
}
