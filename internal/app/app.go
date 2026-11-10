// Package app builds the dependency graph: the shared infrastructure, the
// repositories over it, and the usecases over those.
//
// This is the composition root. It is the only place in the system that names a
// concrete driver (mysql, local); everything below receives its dependencies as
// interfaces, which is what lets a test pass a mock instead
// (docs/ARCHITECTURE.md §7).
//
// It is a package rather than a file in cmd so that the end-to-end tests wire
// the system exactly as the binary does. Wiring a test differently from
// production is how an end-to-end suite starts proving the wrong thing.
package app

import (
	"database/sql"
	"log/slog"
	"time"

	"vending/internal/common/clock"
	"vending/internal/common/config"
	"vending/internal/httpapi"
	"vending/internal/repository/imagestore"
	imagestorelocal "vending/internal/repository/imagestore/local"
	"vending/internal/repository/machine"
	machinemysql "vending/internal/repository/machine/mysql"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/uow"
	uowmysql "vending/internal/repository/uow/mysql"
	"vending/internal/usecase/catalog"
)

// Deps is the built graph.
type Deps struct {
	Runner *mysqldb.Runner

	// repositories, bound to the pool for reads that need no transaction
	UOW      uow.Repository
	Machines machine.Repository
	Images   imagestore.Repository

	// usecases
	Catalog catalog.UseCase
}

// Wire builds the graph. Settings in, usable system out. now is the clock the
// system reads, or nil for the real one.
func Wire(db *sql.DB, cfg config.Config, log *slog.Logger, now func() time.Time) Deps {
	var d Deps
	d.Runner = mysqldb.NewRunner(db, log)

	d.UOW = uowmysql.New(d.Runner)
	d.Machines = machinemysql.New(db)
	d.Images = imagestorelocal.New(cfg.UploadDir)

	cl := clock.New(now)
	d.Catalog = catalog.New(d.UOW, d.Machines, cl, cfg.PaymentWindow, log)
	return d
}

// UseCases is what the delivery layer depends on.
func (d Deps) UseCases() httpapi.UseCases {
	return httpapi.UseCases{Catalog: d.Catalog}
}
