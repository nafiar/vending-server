// Package config reads the server settings from environment variables,
// optionally loaded from a .env file in the working directory.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // Windows has no zoneinfo database; embed it so Asia/Jakarta always loads.
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

	DemoMode bool // DEMO_MODE: shows "Reset data demo" in the admin web; never on a real deployment
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

// The defaults for the behaviour the PRD fixes. They are named, rather than
// written inline where they are read, because more than one place needs them:
// Load starts from them, and so does any test that builds a Config by hand.
const (
	DefaultAdminIdle     = 30 * time.Minute // BR-13
	DefaultOperatorIdle  = 2 * time.Minute  // BR-13: a kiosk is a screen in public
	DefaultPaymentWindow = 60 * time.Second // BR-06
	DefaultTimezone      = "Asia/Jakarta"   // calendar days and hours for recaps (WIB)
)

// Defaults is a Config with the behavioural settings at their PRD values and
// nothing read from the environment.
//
// Anything building a Config without Load should start here. A zero AdminIdle
// means every admin session is idle the moment it is issued, and a zero
// PaymentWindow means nothing can ever be paid for: both show up as a puzzling
// 401 or 410 far from the line that caused them.
func Defaults() Config {
	loc, _ := time.LoadLocation(DefaultTimezone)
	return Config{
		Location:      loc,
		AdminIdle:     DefaultAdminIdle,
		OperatorIdle:  DefaultOperatorIdle,
		PaymentWindow: DefaultPaymentWindow,
	}
}

// Load reads the configuration from the environment, starting from Defaults.
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
	tz := env("TIMEZONE", DefaultTimezone)
	if c.Location, err = time.LoadLocation(tz); err != nil {
		return c, fmt.Errorf("TIMEZONE %q: %w", tz, err)
	}
	if c.AutoMigrate, err = boolEnv("AUTO_MIGRATE", true); err != nil {
		return c, err
	}
	if c.DemoMode, err = boolEnv("DEMO_MODE", false); err != nil {
		return c, err
	}
	if c.AdminIdle, err = durationEnv("ADMIN_SESSION_IDLE", DefaultAdminIdle); err != nil {
		return c, err
	}
	if c.OperatorIdle, err = durationEnv("OPERATOR_SESSION_IDLE", DefaultOperatorIdle); err != nil {
		return c, err
	}
	if c.PaymentWindow, err = durationEnv("PAYMENT_WINDOW", DefaultPaymentWindow); err != nil {
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
