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
	deliveryhttp "vending/internal/delivery/http"
	"vending/internal/repository/imagestore"
	imagestorelocal "vending/internal/repository/imagestore/local"
	"vending/internal/repository/machine"
	machinemysql "vending/internal/repository/machine/mysql"
	"vending/internal/repository/mysqldb"
	"vending/internal/repository/product"
	productmysql "vending/internal/repository/product/mysql"
	"vending/internal/repository/report"
	reportmysql "vending/internal/repository/report/mysql"
	"vending/internal/repository/restock"
	restockmysql "vending/internal/repository/restock/mysql"
	"vending/internal/repository/session"
	sessionmysql "vending/internal/repository/session/mysql"
	"vending/internal/repository/slot"
	slotmysql "vending/internal/repository/slot/mysql"
	"vending/internal/repository/transaction"
	transactionmysql "vending/internal/repository/transaction/mysql"
	"vending/internal/repository/uow"
	uowmysql "vending/internal/repository/uow/mysql"
	"vending/internal/repository/user"
	usermysql "vending/internal/repository/user/mysql"
	"vending/internal/usecase/auth"
	"vending/internal/usecase/catalog"
	machineuc "vending/internal/usecase/machine"
	"vending/internal/usecase/operator"
	productuc "vending/internal/usecase/product"
	"vending/internal/usecase/purchase"
	restockuc "vending/internal/usecase/restock"
)

// Deps is the built graph.
type Deps struct {
	Runner *mysqldb.Runner

	// repositories, bound to the pool for reads that need no transaction
	UOW          uow.Repository
	Machines     machine.Repository
	Transactions transaction.Repository
	Sessions     session.Repository
	Slots        slot.Repository
	Products     product.Repository
	Reports      report.Repository
	Users        user.Repository
	Restocks     restock.Repository
	Images       imagestore.Repository

	// usecases
	Auth     auth.UseCase
	Catalog  catalog.UseCase
	Machine  machineuc.UseCase
	Operator operator.UseCase
	// OperatorConsole is the account management only the command line gets: it
	// can make an admin, which the API must never be able to do.
	OperatorConsole operator.Console
	Product         productuc.UseCase
	Purchase        purchase.UseCase
	Restock         restockuc.UseCase
}

// Wire builds the graph. Settings in, usable system out. now is the clock the
// system reads, or nil for the real one.
func Wire(db *sql.DB, cfg config.Config, log *slog.Logger, now func() time.Time) Deps {
	var d Deps
	d.Runner = mysqldb.NewRunner(db, log)

	d.UOW = uowmysql.New(d.Runner)
	d.Machines = machinemysql.New(db)
	d.Transactions = transactionmysql.New(db)
	d.Sessions = sessionmysql.New(db)
	d.Slots = slotmysql.New(db)
	d.Products = productmysql.New(db)
	d.Reports = reportmysql.New(db)
	d.Users = usermysql.New(db)
	d.Restocks = restockmysql.New(db)
	d.Images = imagestorelocal.New(cfg.UploadDir)

	cl := clock.New(now)
	d.Auth = auth.New(d.UOW, d.Sessions, d.Machines, cl, cfg.AdminIdle, cfg.OperatorIdle, log)
	cat := catalog.New(d.UOW, d.Machines, cl, cfg.PaymentWindow, log)
	d.Catalog = cat
	// A refused stale purchase answers with the fresh catalog, so purchase
	// depends on the catalog usecase rather than reading the slots again itself.
	d.Purchase = purchase.New(d.UOW, d.Transactions, cat, cl, cfg.PaymentWindow, log)
	d.Restock = restockuc.New(d.UOW, d.Slots, cl, log)
	d.Product = productuc.New(d.UOW, d.Products, d.Images, cl, log)
	d.Machine = machineuc.New(d.UOW, d.Machines, d.Slots, d.Reports, cl, cfg.Location, log)
	ops := operator.New(d.UOW, d.Users, d.Reports, cl, cfg.Location, log)
	d.Operator, d.OperatorConsole = ops, ops
	return d
}

// UseCases is what the delivery layer depends on.
func (d Deps) UseCases() deliveryhttp.UseCases {
	return deliveryhttp.UseCases{
		Auth: d.Auth, Catalog: d.Catalog, Machine: d.Machine,
		Operator: d.Operator, Product: d.Product,
		Purchase: d.Purchase, Restock: d.Restock,
	}
}
