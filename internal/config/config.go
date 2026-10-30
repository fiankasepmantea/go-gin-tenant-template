// Package config loads and validates the application configuration from environment variables.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is the entire application configuration.
type Config struct {
	Env      string // development | production | testing
	Port     string
	LogLevel string // debug | info | warn | error
	DB       Database
	Auth     Auth
	Tenant   Tenant
}

// Database holds the PostgreSQL connection settings.
type Database struct {
	Host            string
	Port            int
	User            string
	Password        string
	Name            string
	Schema          string
	SSLMode         string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// Auth holds the PASETO token settings.
type Auth struct {
	PasetoKeyHex string // 32 bytes in hex (64 characters)
	Issuer       string
	AccessTTL    time.Duration
	RefreshTTL   time.Duration
}

// Tenant holds the settings for identifying the tenant from a request.
type Tenant struct {
	BaseDomain string // e.g. "app.com": tokoa.app.com is recognized as tenant "tokoa"
	Header     string // header name for mobile/API clients
}

// IsProduction reports whether the application is running in production mode.
func (c *Config) IsProduction() bool { return c.Env == "production" }

// Modes known to the application. The value also determines the env file name: .env.<mode>.
const (
	ModeDevelopment = "development"
	ModeProduction  = "production"
	ModeTesting     = "testing"
)

func validMode(m string) bool {
	return m == ModeDevelopment || m == ModeProduction || m == ModeTesting
}

// Load picks the mode from APP_ENV (default development), reads the env files for that mode,
// then validates the configuration.
//
// Priority order (higher wins):
//  1. process environment (Kubernetes ConfigMap/Secret, `export`, CI)
//  2. .env.<mode>   e.g. .env.development, .env.production
//  3. .env          local fallback
//
// All files are optional: in a production container there is usually only the process environment.
func Load() (*Config, error) {
	return loadFrom(".")
}

func loadFrom(dir string) (*Config, error) {
	mode := strings.TrimSpace(os.Getenv("APP_ENV"))
	if mode == "" {
		mode = ModeDevelopment
	}
	// Validated before being used to build a file name, so APP_ENV="../x" cannot select another file.
	if !validMode(mode) {
		return nil, fmt.Errorf("invalid configuration: unknown APP_ENV %q (development|production|testing)", mode)
	}

	// godotenv does not override variables already present in the process environment.
	for _, name := range []string{".env." + mode, ".env"} {
		if err := loadFile(filepath.Join(dir, name)); err != nil {
			return nil, err
		}
	}
	return fromEnv()
}

func loadFile(path string) error {
	err := godotenv.Load(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("config: failed to read %s: %w", path, err)
}

func fromEnv() (*Config, error) {
	l := &loader{}

	cfg := &Config{
		Env:      l.str("APP_ENV", "development"),
		Port:     l.str("APP_PORT", "8080"),
		LogLevel: l.str("LOG_LEVEL", "info"),
		DB: Database{
			Host:            l.str("DB_HOST", "localhost"),
			Port:            l.int("DB_PORT", 5432),
			User:            l.str("DB_USER", ""),
			Password:        l.str("DB_PASSWORD", ""),
			Name:            l.str("DB_NAME", ""),
			Schema:          l.str("DB_SCHEMA", "public"),
			SSLMode:         l.str("DB_SSLMODE", "disable"),
			MaxOpenConns:    l.int("DB_MAX_OPEN_CONNS", 25),
			MaxIdleConns:    l.int("DB_MAX_IDLE_CONNS", 5),
			ConnMaxLifetime: l.dur("DB_CONN_MAX_LIFETIME", 30*time.Minute),
		},
		Auth: Auth{
			PasetoKeyHex: l.str("PASETO_KEY", ""),
			Issuer:       l.str("TOKEN_ISSUER", "go-gin-tenant-template"),
			AccessTTL:    l.dur("ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTTL:   l.dur("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		},
		Tenant: Tenant{
			BaseDomain: l.str("TENANT_BASE_DOMAIN", ""),
			Header:     l.str("TENANT_HEADER", "X-Tenant-Code"),
		},
	}

	errs := append([]error{}, l.errs...)
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// Validate checks required settings and sensible values.
func (c *Config) Validate() error {
	var errs []error

	if !validMode(c.Env) {
		errs = append(errs, fmt.Errorf("unknown APP_ENV %q (development|production|testing)", c.Env))
	}
	if c.IsProduction() {
		// Extra rules so that local configuration does not accidentally carry over to production.
		if c.DB.Password == "" {
			errs = append(errs, errors.New("DB_PASSWORD is required in production"))
		}
		if c.DB.SSLMode == "disable" {
			errs = append(errs, errors.New("DB_SSLMODE=disable is not allowed in production (use require or verify-full)"))
		}
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("unknown LOG_LEVEL %q (debug|info|warn|error)", c.LogLevel))
	}
	if c.DB.User == "" {
		errs = append(errs, errors.New("DB_USER is required"))
	}
	if c.DB.Name == "" {
		errs = append(errs, errors.New("DB_NAME is required"))
	}
	if b, err := hex.DecodeString(c.Auth.PasetoKeyHex); err != nil || len(b) != 32 {
		errs = append(errs, errors.New("PASETO_KEY must be 32 bytes in hex (64 characters), generate one with: openssl rand -hex 32"))
	}
	if c.Auth.AccessTTL <= 0 {
		errs = append(errs, errors.New("ACCESS_TOKEN_TTL must be greater than 0"))
	}
	if c.Auth.RefreshTTL <= c.Auth.AccessTTL {
		errs = append(errs, errors.New("REFRESH_TOKEN_TTL must be greater than ACCESS_TOKEN_TTL"))
	}
	return errors.Join(errs...)
}

// loader reads env values while collecting all parse errors at once.
type loader struct{ errs []error }

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func (l *loader) int(key string, def int) int {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s must be a number, got %q", key, raw))
		return def
	}
	return n
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s must be a duration such as 15m or 720h, got %q", key, raw))
		return def
	}
	return d
}
