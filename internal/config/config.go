// Package config loads and validates the application configuration from environment variables.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	Redis    Redis
	Limits   RateLimit
	SMTP     SMTP
	Password Password
	GRPC     GRPC
	TenantDB TenantDB
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

// TenantDB holds the dedicated databases of tenants that do not live in the shared database
// (docs/database-per-tenant.md). Empty by default: every tenant is served by DB_*.
type TenantDB struct {
	URLs         map[string]string // reference -> connection URL, from TENANT_DB_URL_<REF> (never stored in the database)
	MaxOpenConns int               // per dedicated database
	MaxIdleConns int
}

// TenantDBRefPattern is the format of a reference: it is stored in tenants.db_ref and is the lower case form of
// the suffix of TENANT_DB_URL_<REF>.
const TenantDBRefPattern = `^[a-z][a-z0-9_]{0,30}$`

var tenantDBRefRe = regexp.MustCompile(TenantDBRefPattern)

// ValidTenantDBRef reports whether ref is a valid reference.
func ValidTenantDBRef(ref string) bool { return tenantDBRefRe.MatchString(ref) }

// tenantDBPrefix is the start of the environment variables that hold dedicated database URLs.
const tenantDBPrefix = "TENANT_DB_URL_"

// GRPC holds the settings of the gRPC AuthService (cmd/grpc). The REST server ignores them.
type GRPC struct {
	Addr           string // listen address, default ":9090"
	TLSCertFile    string // server certificate (PEM)
	TLSKeyFile     string // its private key (PEM)
	ClientCAFile   string // when set, clients must present a certificate signed by this CA (mTLS)
	AllowPlaintext bool   // production only: serve without TLS (for example behind a service mesh that does mTLS)
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

// Redis holds the connection to Redis (used for rate limiting). Empty Addr = no Redis: counters then live in the
// memory of the process, which is only correct with a single replica.
type Redis struct {
	Addr     string // host:port
	Password string
	DB       int
}

// RateLimit holds the limits of failed login attempts, see session.Limits.
type RateLimit struct {
	Attempts int
	Window   time.Duration
}

// SMTP holds the mail server for transactional e-mail (password reset). Empty Host = mail is not sent:
// in development the message is written to the log, in production the reset endpoint cannot deliver.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	Security string // starttls | tls | none
}

// Enabled reports whether an SMTP server is configured.
func (s SMTP) Enabled() bool { return s.Host != "" }

// Password holds the settings of password reset.
type Password struct {
	AppName  string        // name shown in the e-mail
	ResetTTL time.Duration // how long a reset link works
	ResetURL string        // front end page; placeholders {token} and {tenant}; empty = the e-mail carries the token
}

// ValidateGRPC checks the settings only cmd/grpc needs. In production the server must use TLS, unless
// GRPC_ALLOW_PLAINTEXT=true says that something else (a service mesh) encrypts the connection.
func (c *Config) ValidateGRPC() error {
	var errs []error
	g := c.GRPC
	if _, port, err := net.SplitHostPort(g.Addr); err != nil || port == "" {
		errs = append(errs, errors.New("GRPC_ADDR must be host:port or :port"))
	}
	if (g.TLSCertFile == "") != (g.TLSKeyFile == "") {
		errs = append(errs, errors.New("GRPC_TLS_CERT_FILE and GRPC_TLS_KEY_FILE must be set together"))
	}
	if g.ClientCAFile != "" && g.TLSCertFile == "" {
		errs = append(errs, errors.New("GRPC_TLS_CLIENT_CA_FILE needs GRPC_TLS_CERT_FILE and GRPC_TLS_KEY_FILE"))
	}
	if c.IsProduction() && g.TLSCertFile == "" && !g.AllowPlaintext {
		errs = append(errs, errors.New("production needs GRPC_TLS_CERT_FILE and GRPC_TLS_KEY_FILE (or GRPC_ALLOW_PLAINTEXT=true behind a service mesh that encrypts)"))
	}
	if g.AllowPlaintext && g.TLSCertFile != "" {
		errs = append(errs, errors.New("GRPC_ALLOW_PLAINTEXT=true contradicts GRPC_TLS_CERT_FILE"))
	}
	return errors.Join(errs...)
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
		Redis: Redis{
			Addr:     l.str("REDIS_ADDR", ""),
			Password: l.str("REDIS_PASSWORD", ""),
			DB:       l.int("REDIS_DB", 0),
		},
		Limits: RateLimit{
			Attempts: l.int("RATE_LIMIT_ATTEMPTS", 5),
			Window:   l.dur("RATE_LIMIT_WINDOW", 15*time.Minute),
		},
		SMTP: SMTP{
			Host:     l.str("SMTP_HOST", ""),
			Port:     l.int("SMTP_PORT", 587),
			Username: l.str("SMTP_USERNAME", ""),
			Password: l.str("SMTP_PASSWORD", ""),
			From:     l.str("SMTP_FROM", ""),
			Security: l.str("SMTP_SECURITY", "starttls"),
		},
		Password: Password{
			AppName:  l.str("APP_NAME", "go-gin-tenant-template"),
			ResetTTL: l.dur("PASSWORD_RESET_TTL", 30*time.Minute),
			ResetURL: l.str("PASSWORD_RESET_URL", ""),
		},
		TenantDB: TenantDB{
			URLs:         l.tenantDBURLs(),
			MaxOpenConns: l.int("TENANT_DB_MAX_OPEN_CONNS", 10),
			MaxIdleConns: l.int("TENANT_DB_MAX_IDLE_CONNS", 2),
		},
		GRPC: GRPC{
			Addr:           l.str("GRPC_ADDR", ":9090"),
			TLSCertFile:    l.str("GRPC_TLS_CERT_FILE", ""),
			TLSKeyFile:     l.str("GRPC_TLS_KEY_FILE", ""),
			ClientCAFile:   l.str("GRPC_TLS_CLIENT_CA_FILE", ""),
			AllowPlaintext: l.bool("GRPC_ALLOW_PLAINTEXT", false),
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
	errs = append(errs, c.validateServices()...)
	errs = append(errs, c.validateTenantDBs()...)
	return errors.Join(errs...)
}

// validateServices checks Redis, rate limiting, SMTP and password reset.
func (c *Config) validateServices() []error {
	var errs []error
	if c.Redis.Addr != "" {
		if host, port, err := net.SplitHostPort(c.Redis.Addr); err != nil || host == "" || port == "" {
			errs = append(errs, errors.New("REDIS_ADDR must be host:port"))
		}
	}
	if c.Redis.DB < 0 {
		errs = append(errs, errors.New("REDIS_DB must not be negative"))
	}
	if c.Limits.Attempts < 1 {
		errs = append(errs, errors.New("RATE_LIMIT_ATTEMPTS must be at least 1"))
	}
	if c.Limits.Window < time.Second {
		errs = append(errs, errors.New("RATE_LIMIT_WINDOW must be at least 1s"))
	}
	if c.Password.ResetTTL < 5*time.Minute || c.Password.ResetTTL > 24*time.Hour {
		errs = append(errs, errors.New("PASSWORD_RESET_TTL must be between 5m and 24h"))
	}
	if c.Password.ResetURL != "" {
		u, err := url.Parse(strings.NewReplacer("{token}", "x", "{tenant}", "x").Replace(c.Password.ResetURL))
		switch {
		case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
			errs = append(errs, errors.New("PASSWORD_RESET_URL must be an http(s) URL, e.g. https://{tenant}.example.com/reset?token={token}"))
		case c.IsProduction() && u.Scheme != "https":
			errs = append(errs, errors.New("PASSWORD_RESET_URL must use https in production"))
		}
	}
	if c.SMTP.Enabled() {
		if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
			errs = append(errs, errors.New("SMTP_PORT must be between 1 and 65535"))
		}
		if c.SMTP.From == "" {
			errs = append(errs, errors.New("SMTP_FROM is required when SMTP_HOST is set"))
		}
		switch c.SMTP.Security {
		case "starttls", "tls":
		case "none":
			if c.IsProduction() {
				errs = append(errs, errors.New("SMTP_SECURITY=none is not allowed in production (use starttls or tls)"))
			}
		default:
			errs = append(errs, fmt.Errorf("unknown SMTP_SECURITY %q (starttls|tls|none)", c.SMTP.Security))
		}
	}
	return errs
}

// validateTenantDBs checks the URLs of dedicated databases. The messages name the variable, never the URL (it holds a password).
func (c *Config) validateTenantDBs() []error {
	var errs []error
	refs := make([]string, 0, len(c.TenantDB.URLs))
	for ref := range c.TenantDB.URLs {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		name := tenantDBPrefix + strings.ToUpper(ref)
		u, err := url.Parse(c.TenantDB.URLs[ref])
		switch {
		case err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || strings.Trim(u.Path, "/") == "":
			errs = append(errs, fmt.Errorf("%s must be a URL like postgres://user:password@host:5432/dbname?sslmode=require", name))
		case c.IsProduction():
			switch u.Query().Get("sslmode") {
			case "require", "verify-ca", "verify-full":
			default:
				errs = append(errs, fmt.Errorf("%s needs sslmode=require (or verify-ca, verify-full) in production", name))
			}
		}
	}
	if c.TenantDB.MaxOpenConns < 1 {
		errs = append(errs, errors.New("TENANT_DB_MAX_OPEN_CONNS must be at least 1"))
	}
	if c.TenantDB.MaxIdleConns < 0 || c.TenantDB.MaxIdleConns > c.TenantDB.MaxOpenConns {
		errs = append(errs, errors.New("TENANT_DB_MAX_IDLE_CONNS must be between 0 and TENANT_DB_MAX_OPEN_CONNS"))
	}
	return errs
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

// tenantDBURLs reads every TENANT_DB_URL_<REF>. The reference is the lower case of <REF>.
func (l *loader) tenantDBURLs() map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		suffix, ok := strings.CutPrefix(name, tenantDBPrefix)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		ref := strings.ToLower(suffix)
		if !ValidTenantDBRef(ref) {
			l.errs = append(l.errs, fmt.Errorf("%s: %q is not a valid reference (lower case letters, digits and _, starting with a letter, at most 31 characters)", name, ref))
			continue
		}
		out[ref] = strings.TrimSpace(value)
	}
	return out
}

func (l *loader) bool(key string, def bool) bool {
	raw := l.str(key, "")
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s must be true or false, got %q", key, raw))
		return def
	}
	return b
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
