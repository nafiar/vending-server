// Package config reads the server settings from environment variables,
// optionally loaded from a .env file in the working directory.
package config

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Windows has no zoneinfo database; embed it so Asia/Jakarta always loads.

	"github.com/go-sql-driver/mysql"
)

// Config holds every setting the server reads at start-up.
type Config struct {
	HTTPAddr string // HTTP_ADDR, default ":8080"
	TLSCert  string // TLS_CERT_FILE, optional
	TLSKey   string // TLS_KEY_FILE, optional

	DB DBConfig

	Location    *time.Location // TIMEZONE, default Asia/Jakarta (WIB)
	UploadDir   string         // UPLOAD_DIR, default data/uploads
	AutoMigrate bool           // AUTO_MIGRATE, default true

	AdminIdle     time.Duration // ADMIN_SESSION_IDLE, default 30m (BR-13)
	OperatorIdle  time.Duration // OPERATOR_SESSION_IDLE, default 2m (BR-13)
	PaymentWindow time.Duration // PAYMENT_WINDOW, default 60s (BR-06)

	LogFormat string // LOG_FORMAT: text (default) or json
	LogLevel  string // LOG_LEVEL: debug, info (default), warn, error
}

// DBConfig describes how to reach MySQL. DB_DSN wins over the separate fields.
type DBConfig struct {
	DSN      string // DB_DSN, e.g. vending:secret@tcp(127.0.0.1:3306)/vending
	Host     string // DB_HOST, default 127.0.0.1
	Port     string // DB_PORT, default 3306
	User     string // DB_USER, default root
	Password string // DB_PASSWORD
	Name     string // DB_NAME, default vending
}

var dbNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Load reads the configuration from the environment.
func Load() (Config, error) {
	c := Config{
		HTTPAddr: env("HTTP_ADDR", ":8080"),
		TLSCert:  env("TLS_CERT_FILE", ""),
		TLSKey:   env("TLS_KEY_FILE", ""),
		DB: DBConfig{
			DSN:      env("DB_DSN", ""),
			Host:     env("DB_HOST", "127.0.0.1"),
			Port:     env("DB_PORT", "3306"),
			User:     env("DB_USER", "root"),
			Password: env("DB_PASSWORD", ""),
			Name:     env("DB_NAME", "vending"),
		},
		UploadDir: env("UPLOAD_DIR", "data/uploads"),
		LogFormat: strings.ToLower(env("LOG_FORMAT", "text")),
		LogLevel:  strings.ToLower(env("LOG_LEVEL", "info")),
	}

	var err error
	tz := env("TIMEZONE", "Asia/Jakarta")
	if c.Location, err = time.LoadLocation(tz); err != nil {
		return c, fmt.Errorf("TIMEZONE %q: %w", tz, err)
	}
	if c.AutoMigrate, err = boolEnv("AUTO_MIGRATE", true); err != nil {
		return c, err
	}
	if c.AdminIdle, err = durationEnv("ADMIN_SESSION_IDLE", 30*time.Minute); err != nil {
		return c, err
	}
	if c.OperatorIdle, err = durationEnv("OPERATOR_SESSION_IDLE", 2*time.Minute); err != nil {
		return c, err
	}
	if c.PaymentWindow, err = durationEnv("PAYMENT_WINDOW", 60*time.Second); err != nil {
		return c, err
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return c, fmt.Errorf("set both TLS_CERT_FILE and TLS_KEY_FILE, or neither")
	}
	if c.DB.DSN == "" && !dbNamePattern.MatchString(c.DB.Name) {
		return c, fmt.Errorf("DB_NAME %q: use letters, digits and underscores only", c.DB.Name)
	}
	return c, nil
}

// MySQL returns the driver configuration. withDB=false leaves the database name
// out, for creating the database itself.
func (d DBConfig) MySQL(withDB bool) (*mysql.Config, error) {
	var mc *mysql.Config
	if d.DSN != "" {
		var err error
		if mc, err = mysql.ParseDSN(d.DSN); err != nil {
			return nil, fmt.Errorf("DB_DSN: %w", err)
		}
	} else {
		mc = mysql.NewConfig()
		mc.User = d.User
		mc.Passwd = d.Password
		mc.Net = "tcp"
		mc.Addr = net.JoinHostPort(d.Host, d.Port)
		mc.DBName = d.Name
	}
	if !withDB {
		mc.DBName = ""
	}
	// Times are stored and compared in UTC (NFR-05); the server converts to WIB for display and grouping.
	mc.ParseTime = true
	mc.Loc = time.UTC
	if mc.Params == nil {
		mc.Params = map[string]string{}
	}
	mc.Params["time_zone"] = "'+00:00'"
	mc.Collation = "utf8mb4_unicode_ci"
	return mc, nil
}

// DatabaseName is the schema the server uses.
func (d DBConfig) DatabaseName() (string, error) {
	mc, err := d.MySQL(true)
	if err != nil {
		return "", err
	}
	if !dbNamePattern.MatchString(mc.DBName) {
		return "", fmt.Errorf("database name %q: use letters, digits and underscores only", mc.DBName)
	}
	return mc.DBName, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func boolEnv(key string, def bool) (bool, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s=%q: want true or false", key, v)
	}
	return b, nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := env(key, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def, fmt.Errorf("%s=%q: want a duration such as 30m or 60s", key, v)
	}
	return d, nil
}
