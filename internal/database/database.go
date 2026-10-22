// Package database opens the MySQL connection pool and applies the schema migrations.
package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"vending/internal/config"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open connects to the configured database and checks that it answers.
func Open(ctx context.Context, dbc config.DBConfig) (*sql.DB, error) {
	mc, err := dbc.MySQL(true)
	if err != nil {
		return nil, err
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to MySQL at %s (database %q): %w", mc.Addr, mc.DBName, err)
	}
	return db, nil
}

// EnsureDatabase creates the database if it does not exist yet. It needs the
// CREATE privilege; when the database already exists it does nothing.
func EnsureDatabase(ctx context.Context, dbc config.DBConfig) error {
	name, err := dbc.DatabaseName()
	if err != nil {
		return err
	}
	mc, err := dbc.MySQL(false)
	if err != nil {
		return err
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return err
	}
	db := sql.OpenDB(conn)
	defer db.Close()

	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// name is checked against ^[A-Za-z0-9_]+$, so quoting it with backticks is safe.
	_, err = db.ExecContext(cctx, "CREATE DATABASE IF NOT EXISTS `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci")
	if err != nil {
		return fmt.Errorf("create database %q at %s: %w", name, mc.Addr, err)
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		num, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must start with a number and an underscore", e.Name())
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies every embedded migration that has not run yet, in order,
// and returns the names it applied. A MySQL advisory lock keeps two servers
// starting at once from applying the same migration twice.
func Migrate(ctx context.Context, db *sql.DB, log *slog.Logger) ([]string, error) {
	migs, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK('vending_migrate', 30)").Scan(&got); err != nil {
		return nil, err
	}
	if got.Int64 != 1 {
		return nil, errors.New("another process is migrating the database; try again")
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('vending_migrate')")

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INT          NOT NULL,
		name       VARCHAR(255) NOT NULL,
		applied_at DATETIME(3)  NOT NULL,
		PRIMARY KEY (version)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		return nil, err
	}

	done := map[int]bool{}
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		done[v] = true
	}
	rows.Close()

	var applied []string
	for _, m := range migs {
		if done[m.version] {
			continue
		}
		for i, stmt := range splitStatements(m.sql) {
			if _, err := conn.ExecContext(ctx, stmt); err != nil {
				return applied, fmt.Errorf("migration %s, statement %d: %w", m.name, i+1, err)
			}
		}
		if _, err := conn.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
			m.version, m.name, time.Now().UTC()); err != nil {
			return applied, err
		}
		log.Info("migration applied", "name", m.name)
		applied = append(applied, m.name)
	}
	return applied, nil
}

// splitStatements cuts a migration file into statements. Each statement ends
// with a semicolon at the end of a line; lines starting with -- are comments.
// The migration files are written to that rule, so no SQL parser is needed.
func splitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if strings.HasSuffix(trimmed, ";") {
			stmt := strings.TrimSpace(cur.String())
			out = append(out, strings.TrimSuffix(stmt, ";"))
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
