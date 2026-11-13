// Package mysql runs a unit of work as one MySQL transaction.
package mysql

import (
	"context"
	"database/sql"

	auditmysql "vending/internal/repository/audit/mysql"
	machinemysql "vending/internal/repository/machine/mysql"
	"vending/internal/repository/mysqldb"
	productmysql "vending/internal/repository/product/mysql"
	restockmysql "vending/internal/repository/restock/mysql"
	sessionmysql "vending/internal/repository/session/mysql"
	slotmysql "vending/internal/repository/slot/mysql"
	transactionmysql "vending/internal/repository/transaction/mysql"
	"vending/internal/repository/uow"
	usermysql "vending/internal/repository/user/mysql"

	"vending/internal/repository/audit"
	"vending/internal/repository/machine"
	"vending/internal/repository/product"
	"vending/internal/repository/restock"
	"vending/internal/repository/session"
	"vending/internal/repository/slot"
	"vending/internal/repository/transaction"
	"vending/internal/repository/user"
)

// MySQL turns a unit of work into one database transaction, with the retry the
// storage engine needs.
type MySQL struct{ runner *mysqldb.Runner }

// Compile-time proof that the driver still matches the port.
var _ uow.Repository = (*MySQL)(nil)

// New returns the driver over the shared transaction runner.
func New(runner *mysqldb.Runner) *MySQL { return &MySQL{runner: runner} }

func (m *MySQL) Do(ctx context.Context, fn func(uow.Tx) error) error {
	return m.runner.Do(ctx, func(tx *sql.Tx) error { return fn(bundle{q: tx}) })
}

func (m *MySQL) Read(ctx context.Context, fn func(uow.Tx) error) error {
	return m.runner.Read(ctx, func(tx *sql.Tx) error { return fn(bundle{q: tx}) })
}

// bundle binds every repository to one transaction. The adapters hold a
// Queryer, so the same types serve a direct read off the pool.
type bundle struct{ q mysqldb.Queryer }

var _ uow.Tx = bundle{}

func (b bundle) Machines() machine.Repository         { return machinemysql.New(b.q) }
func (b bundle) Slots() slot.Repository               { return slotmysql.New(b.q) }
func (b bundle) Transactions() transaction.Repository { return transactionmysql.New(b.q) }
func (b bundle) Users() user.Repository               { return usermysql.New(b.q) }
func (b bundle) Restocks() restock.Repository         { return restockmysql.New(b.q) }
func (b bundle) Products() product.Repository         { return productmysql.New(b.q) }
func (b bundle) Sessions() session.Repository         { return sessionmysql.New(b.q) }
func (b bundle) Audit() audit.Repository              { return auditmysql.New(b.q) }
