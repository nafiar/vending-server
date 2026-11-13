// Package health asks whether the things the server depends on are reachable.
// The port is here; the drivers live in subpackages.
//
// It is a repository like any other, even though it reads no data: "can I still
// reach the database" is a question about infrastructure, and the delivery layer
// that answers /api/health should no more hold a connection pool to find out
// than a handler should hold one to list products.
package health

import "context"

//go:generate mockgen -source=health.go -destination=mock/health_mock.go -package=mock

// Repository is the dependency check.
type Repository interface {
	// Ping reports whether the store is reachable right now. The caller gives
	// it a deadline: a health check that waits is itself a failure.
	Ping(ctx context.Context) error
}
