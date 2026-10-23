// Command vending-server runs the server of the vending machine information
// system: the admin web, its API, and the API the Kiosk Agent on every machine calls.
//
//	vending-server                 start the server (same as "serve")
//	vending-server migrate         create the database if needed and apply the schema
//	vending-server create-admin    add the first admin account (or reset its password)
//	vending-server seed-demo       load three demo machines with 90 days of history
//	vending-server rotate-key      print a new key for a machine
//	vending-server reconcile       check every slot's count against visits and sales
//
// Settings come from environment variables, or from a .env file in the
// working directory; see .env.example.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"vending/internal/config"
	"vending/internal/core"
	"vending/internal/database"
	"vending/internal/httpapi"
	"vending/internal/security"
	"vending/internal/seed"
	"vending/web"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

const usage = `Usage: vending-server [command] [flags]

Commands:
  serve          start the server (default)
  migrate        create the database if needed and apply the schema
  create-admin   add an admin account: -username admin -name "Nama" [-password ...] [-reset]
  seed-demo      load demo data into an empty database [-reset deletes every row first]
  reset          delete every row and load the demo data again; machine keys stay the same
  rotate-key     print a new key for a machine: -machine VM-01
  reconcile      check every slot's count against its visits and sales
  version        print the version

Settings are read from environment variables or a .env file; see .env.example.
`

func run(args []string) error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args)
	case "migrate":
		return migrateCmd(args)
	case "create-admin":
		return createAdmin(args)
	case "seed-demo":
		return seedDemo(args)
	case "reset":
		return seedDemo(append([]string{"-reset"}, args...))
	case "rotate-key":
		return rotateKey(args)
	case "reconcile":
		return reconcile(args)
	case "version":
		fmt.Println("vending-server", version)
		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

type app struct {
	cfg config.Config
	log *slog.Logger
	db  *sql.DB
	svc *core.Service
}

// open loads the settings, connects to MySQL and, unless AUTO_MIGRATE=false,
// creates the database and applies pending migrations first.
func open(ctx context.Context, forceMigrate bool) (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	log := newLogger(cfg)
	var ensureErr error
	if cfg.AutoMigrate || forceMigrate {
		// Needs the CREATE privilege; a database created beforehand by a DBA works too.
		ensureErr = database.EnsureDatabase(ctx, cfg.DB)
	}
	db, err := database.Open(ctx, cfg.DB)
	if err != nil {
		if ensureErr != nil {
			return nil, ensureErr
		}
		return nil, err
	}
	if cfg.AutoMigrate || forceMigrate {
		if _, err := database.Migrate(ctx, db, log); err != nil {
			db.Close()
			return nil, err
		}
	}
	svc := core.New(db, core.Options{
		Location:      cfg.Location,
		UploadDir:     cfg.UploadDir,
		AdminIdle:     cfg.AdminIdle,
		OperatorIdle:  cfg.OperatorIdle,
		PaymentWindow: cfg.PaymentWindow,
		Logger:        log,
	})
	return &app{cfg: cfg, log: log, db: db, svc: svc}, nil
}

func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// ---------------------------------------------------------------- serve

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "listen address, overrides HTTP_ADDR (e.g. :8080)")
	_ = fs.Parse(args)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer a.db.Close()
	if *addr != "" {
		a.cfg.HTTPAddr = *addr
	}
	if err := os.MkdirAll(a.cfg.UploadDir, 0o755); err != nil {
		return fmt.Errorf("upload directory %s: %w", a.cfg.UploadDir, err)
	}

	var admins int
	if err := a.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'ADMIN' AND is_active`).Scan(&admins); err == nil && admins == 0 {
		a.log.Warn("no admin account yet: run `vending-server create-admin` (or `seed-demo` for demo data)")
	}

	if n, err := a.svc.ExpirePending(ctx); err == nil && n > 0 {
		a.log.Info("expired abandoned payments left from the last run", "count", n)
	}
	go a.svc.RunJobs(ctx)

	var opts httpapi.Options
	if a.cfg.DemoMode {
		opts.DemoReset = func(ctx context.Context) error {
			_, err := seed.Demo(ctx, a.svc, true)
			return err
		}
		a.log.Warn("DEMO_MODE is on: any admin can reset all data from the admin web")
	}
	srv := &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           httpapi.New(a.svc, web.Admin(), a.log, opts),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		if a.cfg.TLSCert != "" {
			errc <- srv.ListenAndServeTLS(a.cfg.TLSCert, a.cfg.TLSKey)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	scheme := "http"
	if a.cfg.TLSCert != "" {
		scheme = "https"
	}
	host := a.cfg.HTTPAddr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	a.log.Info("server started", "version", version, "admin_web", scheme+"://"+host+"/", "time_zone", a.cfg.Location.String())

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		a.log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// ---------------------------------------------------------------- tools

func migrateCmd(args []string) error {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	ensureErr := database.EnsureDatabase(ctx, cfg.DB)
	db, err := database.Open(ctx, cfg.DB)
	if err != nil {
		if ensureErr != nil {
			return ensureErr
		}
		return err
	}
	defer db.Close()
	applied, err := database.Migrate(ctx, db, log)
	if err != nil {
		return err
	}
	name, _ := cfg.DB.DatabaseName()
	if len(applied) == 0 {
		fmt.Printf("Database %q is up to date.\n", name)
	} else {
		fmt.Printf("Database %q: applied %s.\n", name, strings.Join(applied, ", "))
	}
	return nil
}

func createAdmin(args []string) error {
	fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
	username := fs.String("username", "admin", "login name")
	name := fs.String("name", "Administrator", "display name")
	password := fs.String("password", "", "password, at least 8 characters (empty: generate one)")
	reset := fs.Bool("reset", false, "if the account exists, set a new password instead")
	_ = fs.Parse(args)

	ctx := context.Background()
	a, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer a.db.Close()
	pw := *password
	generated := pw == ""
	if generated {
		pw = security.TemporaryPassword(14)
	}
	if *reset {
		if err := a.svc.SetPassword(ctx, *username, pw); err == nil {
			printPassword("Password reset for", *username, pw, generated)
			return nil
		} else if e, ok := core.AsError(err); !ok || e.Code != "NOT_FOUND" {
			return describe(err)
		}
	}
	if _, err := a.svc.CreateAdmin(ctx, *name, *username, pw); err != nil {
		return describe(err)
	}
	printPassword("Admin account created:", *username, pw, generated)
	return nil
}

func printPassword(what, username, pw string, generated bool) {
	fmt.Printf("%s %s\n", what, username)
	if generated {
		fmt.Printf("Password: %s\n(shown once; keep it somewhere safe)\n", pw)
	}
}

func seedDemo(args []string) error {
	fs := flag.NewFlagSet("seed-demo", flag.ExitOnError)
	reset := fs.Bool("reset", false, "delete every row in the database first")
	_ = fs.Parse(args)

	ctx := context.Background()
	a, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer a.db.Close()
	if err := os.MkdirAll(a.cfg.UploadDir, 0o755); err != nil {
		return err
	}
	res, err := seed.Demo(ctx, a.svc, *reset)
	if err != nil {
		return err
	}
	fmt.Printf("Demo data loaded: %d transactions and %d restock visits on 3 machines.\n\nAccounts (demo passwords, change them before real use):\n", res.Transactions, res.Visits)
	for _, u := range res.Users {
		fmt.Println("  " + u)
	}
	fmt.Println("\nMachine keys (only their hashes are stored, so a new key is shown once):")
	codes := make([]string, 0, len(res.MachineKeys))
	for c := range res.MachineKeys {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	for _, c := range codes {
		key := res.MachineKeys[c]
		if key == "" {
			key = "(unchanged: the kiosk keeps the key it already has)"
		}
		fmt.Printf("  %s  %s\n", c, key)
	}
	return nil
}

func rotateKey(args []string) error {
	fs := flag.NewFlagSet("rotate-key", flag.ExitOnError)
	code := fs.String("machine", "", "machine code, e.g. VM-01")
	_ = fs.Parse(args)
	if *code == "" {
		return errors.New("rotate-key needs -machine CODE")
	}
	ctx := context.Background()
	a, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer a.db.Close()
	r, err := a.svc.RotateMachineKeyByCode(ctx, *code)
	if err != nil {
		return describe(err)
	}
	fmt.Printf("New key for %s (the old key no longer works):\n%s\n", r.Machine.Code, r.APIKey)
	return nil
}

func reconcile(args []string) error {
	ctx := context.Background()
	a, err := open(ctx, false)
	if err != nil {
		return err
	}
	defer a.db.Close()
	rows, err := a.svc.Reconcile(ctx)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "MACHINE\tSLOT\tAT LAST VISIT\tSOLD SINCE\tEXPECTED\tACTUAL\tOK")
	bad := 0
	for _, r := range rows {
		ok := "yes"
		if !r.Matches {
			ok, bad = "NO", bad+1
		}
		visit := fmt.Sprint(r.CountAtVisit)
		if r.NeverRestocked {
			visit = "never restocked"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%d\t%d\t%s\n", r.MachineCode, r.SlotNumber, visit, r.SoldSince, r.Expected, r.Actual, ok)
	}
	tw.Flush()
	if bad > 0 {
		return fmt.Errorf("%d slot(s) don't match their history", bad)
	}
	fmt.Printf("All %d slots match their visits and sales.\n", len(rows))
	return nil
}

// describe turns a validation error into a readable line for the terminal.
func describe(err error) error {
	e, ok := core.AsError(err)
	if !ok {
		return err
	}
	if len(e.Fields) == 0 {
		return errors.New(e.Message)
	}
	var parts []string
	for k, v := range e.Fields {
		parts = append(parts, k+": "+v)
	}
	sort.Strings(parts)
	return errors.New(strings.Join(parts, "; "))
}
