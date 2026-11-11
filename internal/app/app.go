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
	"vending/internal/repository/audit"
	auditmysql "vending/internal/repository/audit/mysql"
	"vending/internal/repository/health"
	healthmysql "vending/internal/repository/health/mysql"
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
	"vending/internal/repository/seeddata"
	seeddatamysql "vending/internal/repository/seeddata/mysql"
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
	audituc "vending/internal/usecase/audit"
	"vending/internal/usecase/auth"
	"vending/internal/usecase/catalog"
	"vending/internal/usecase/demo"
	machineuc "vending/internal/usecase/machine"
	"vending/internal/usecase/maintenance"
	"vending/internal/usecase/operator"
	productuc "vending/internal/usecase/product"
	"vending/internal/usecase/purchase"
	"vending/internal/usecase/recap"
	restockuc "vending/internal/usecase/restock"
)

// Deps is the built graph.
type Deps struct {
	Runner *mysqldb.Runner

	// The repositories, bound to the pool. Anything that has to commit
	// together goes through UOW instead, which hands out the same ports bound
	// to one transaction.
	UOW      uow.Repository
	Machines machine.Repository
	Slots    slot.Repository
	Products product.Repository
	Users    user.Repository
	Sessions session.Repository
	Restocks restock.Repository

	Transactions transaction.Repository
	Reports      report.Repository
	Images       imagestore.Repository
	Store        health.Repository
	SeedData     seeddata.Repository

	// The read-only halves of three ports whose write side is above. Each is
	// the same driver, bound to the pool: a reader is not given the methods
	// that write (repository/audit, repository/transaction, repository/restock).
	AuditLogs      audit.Reader
	History        transaction.History
	RestockHistory restock.History

	// The usecases.
	Audit       audituc.UseCase
	Auth        auth.UseCase
	Catalog     catalog.UseCase
	Demo        demo.UseCase
	Machine     machineuc.UseCase
	Maintenance maintenance.UseCase
	Operator    operator.UseCase
	Product     productuc.UseCase
	Purchase    purchase.UseCase
	Recap       recap.UseCase
	Restock     restockuc.UseCase

	// OperatorConsole is the account management only the command line gets: it
	// can make an admin, which the API must never be able to do.
	OperatorConsole operator.Console
}

// Wire builds the graph. Settings in, usable system out. now is the clock the
// system reads, or nil for the real one.
func Wire(db *sql.DB, cfg config.Config, log *slog.Logger, now func() time.Time) Deps {
	var d Deps
	d.Runner = mysqldb.NewRunner(db, log)

	d.UOW = uowmysql.New(d.Runner)
	d.AuditLogs = auditmysql.New(db)
	d.Machines = machinemysql.New(db)
	// One struct, two ports: the state-machine steps and the read-only history.
	txs := transactionmysql.New(db)
	d.Transactions, d.History = txs, txs
	d.Sessions = sessionmysql.New(db)
	d.Slots = slotmysql.New(db)
	d.Products = productmysql.New(db)
	d.Reports = reportmysql.New(db)
	d.Users = usermysql.New(db)
	d.Store = healthmysql.New(db)
	d.SeedData = seeddatamysql.New(db)
	// One struct, two ports: the write side of a visit and the read-only history.
	restocks := restockmysql.New(db)
	d.Restocks, d.RestockHistory = restocks, restocks
	d.Images = imagestorelocal.New(cfg.UploadDir)

	cl := clock.New(now)

	d.Audit = audituc.New(d.AuditLogs, cl, cfg.Location, log)
	// The sweeps and the two checks the clock asks for, not a person.
	d.Maintenance = maintenance.New(d.Transactions, d.Sessions, d.Reports, d.Store, cl,
		cfg.PaymentWindow, cfg.AdminIdle, cfg.OperatorIdle, log)
	d.Auth = auth.New(d.UOW, d.Sessions, d.Machines, cl, cfg.AdminIdle, cfg.OperatorIdle, log)
	cat := catalog.New(d.UOW, d.Machines, cl, cfg.PaymentWindow, log)
	d.Catalog = cat
	// A refused stale purchase answers with the fresh catalog, so purchase
	// depends on the catalog usecase rather than reading the slots again itself.
	d.Purchase = purchase.New(d.UOW, d.Transactions, cat, cl, cfg.PaymentWindow, log)
	d.Restock = restockuc.New(d.UOW, d.Slots, d.RestockHistory, cl, cfg.Location, log)
	d.Recap = recap.New(d.Reports, d.History, cl, cfg.Location, log)
	d.Product = productuc.New(d.UOW, d.Products, d.Images, cl, log)
	d.Machine = machineuc.New(d.UOW, d.Machines, d.Slots, d.Reports, cl, cfg.Location, log)
	ops := operator.New(d.UOW, d.Users, d.Reports, cl, cfg.Location, log)
	d.Operator, d.OperatorConsole = ops, ops
	// The demo load is not part of the API's usecases: it is reachable only
	// from the command line and, on a demo server, from one admin endpoint.
	d.Demo = demo.New(d.SeedData, d.Images, cl, cfg.Location)
	return d
}

// UseCases is what the delivery layer depends on.
func (d Deps) UseCases() deliveryhttp.UseCases {
	return deliveryhttp.UseCases{
		Audit: d.Audit, Auth: d.Auth, Catalog: d.Catalog, Machine: d.Machine,
		Maintenance: d.Maintenance,
		Operator:    d.Operator, Product: d.Product, Recap: d.Recap,
		Purchase: d.Purchase, Restock: d.Restock,
	}
}
