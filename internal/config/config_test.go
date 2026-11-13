package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_NAME", "tenantdb")
	t.Setenv("PASETO_KEY", validKey)
}

func TestFromEnv_Defaults(t *testing.T) {
	setValidEnv(t)

	cfg, err := fromEnv()
	if err != nil {
		t.Fatalf("fromEnv() error = %v", err)
	}
	if cfg.Port != "8080" || cfg.DB.Port != 5432 || cfg.DB.Schema != "public" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Auth.AccessTTL != 15*time.Minute {
		t.Errorf("AccessTTL = %v", cfg.Auth.AccessTTL)
	}
	if cfg.Tenant.Header != "X-Tenant-Code" {
		t.Errorf("Tenant.Header = %q", cfg.Tenant.Header)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if cfg.IsProduction() {
		t.Error("default env must not be production")
	}
}

func TestFromEnv_Invalid(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"empty key", map[string]string{"PASETO_KEY": ""}, "PASETO_KEY"},
		{"key too short", map[string]string{"PASETO_KEY": "abcd"}, "PASETO_KEY"},
		{"key is not hex", map[string]string{"PASETO_KEY": strings.Repeat("z", 64)}, "PASETO_KEY"},
		{"db port is not a number", map[string]string{"DB_PORT": "abc"}, "DB_PORT"},
		{"invalid duration", map[string]string{"ACCESS_TOKEN_TTL": "five minutes"}, "ACCESS_TOKEN_TTL"},
		{"refresh <= access", map[string]string{"ACCESS_TOKEN_TTL": "1h", "REFRESH_TOKEN_TTL": "30m"}, "REFRESH_TOKEN_TTL"},
		{"unknown env", map[string]string{"APP_ENV": "staging"}, "APP_ENV"},
		{"invalid log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"empty db user", map[string]string{"DB_USER": ""}, "DB_USER"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := fromEnv()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %s", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_ProductionRules(t *testing.T) {
	setValidEnv(t)
	t.Setenv("APP_ENV", "production")

	// no password and sslmode disable: two errors at once
	_, err := fromEnv()
	if err == nil || !strings.Contains(err.Error(), "DB_PASSWORD") || !strings.Contains(err.Error(), "DB_SSLMODE") {
		t.Fatalf("production must reject an empty password and sslmode=disable, got: %v", err)
	}

	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_SSLMODE", "require")
	cfg, err := fromEnv()
	if err != nil {
		t.Fatalf("valid production config was rejected: %v", err)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() must be true")
	}

	// development is still allowed without a password and without ssl
	t.Setenv("APP_ENV", "development")
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("DB_SSLMODE", "disable")
	if _, err := fromEnv(); err != nil {
		t.Errorf("development should be lenient: %v", err)
	}
}

// isolate prevents godotenv from leaking variables into other tests.
func isolate(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFrom_PicksFileByMode(t *testing.T) {
	isolate(t, "APP_ENV", "APP_PORT", "DB_USER", "DB_NAME", "DB_PASSWORD", "DB_SSLMODE", "PASETO_KEY")
	dir := t.TempDir()
	base := "DB_USER=u\nDB_NAME=n\nPASETO_KEY=" + validKey + "\n"
	write(t, dir, ".env.development", base+"APP_PORT=1111\n")
	write(t, dir, ".env.production", base+"APP_PORT=2222\nDB_PASSWORD=p\nDB_SSLMODE=require\n")

	// without APP_ENV: development
	cfg, err := loadFrom(dir)
	if err != nil || cfg.Port != "1111" || cfg.Env != "development" {
		t.Fatalf("development: cfg=%+v err=%v", cfg, err)
	}

	isolate(t, "APP_PORT", "DB_USER", "DB_NAME", "DB_PASSWORD", "DB_SSLMODE", "PASETO_KEY")
	t.Setenv("APP_ENV", "production")
	cfg, err = loadFrom(dir)
	if err != nil || cfg.Port != "2222" || !cfg.IsProduction() {
		t.Fatalf("production: cfg=%+v err=%v", cfg, err)
	}
}

func TestLoadFrom_Priority(t *testing.T) {
	isolate(t, "APP_ENV", "APP_PORT", "DB_USER", "DB_NAME", "PASETO_KEY", "LOG_LEVEL")
	dir := t.TempDir()
	write(t, dir, ".env.development", "DB_USER=u\nDB_NAME=n\nPASETO_KEY="+validKey+"\nAPP_PORT=1111\n")
	write(t, dir, ".env", "APP_PORT=9999\nLOG_LEVEL=warn\n")

	// process environment overrides files
	t.Setenv("APP_PORT", "7777")
	cfg, err := loadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "7777" {
		t.Errorf("process environment must win, Port = %q", cfg.Port)
	}
	// .env.<mode> overrides .env; .env fills in the blanks
	if cfg.LogLevel != "warn" {
		t.Errorf(".env must act as a fallback, LogLevel = %q", cfg.LogLevel)
	}
}

func TestLoadFrom_RejectsUnknownMode(t *testing.T) {
	for _, mode := range []string{"../../etc/passwd", "staging", "prod"} {
		t.Setenv("APP_ENV", mode)
		if _, err := loadFrom(t.TempDir()); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
			t.Errorf("mode %q should be rejected, err=%v", mode, err)
		}
	}
}

func TestLoadFrom_NoFilesIsFine(t *testing.T) {
	// In a production container there are no env files: everything comes from the process environment.
	setValidEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_PASSWORD", "p")
	t.Setenv("DB_SSLMODE", "require")
	if _, err := loadFrom(t.TempDir()); err != nil {
		t.Fatalf("should still work without env files: %v", err)
	}
}

func TestServiceSettings(t *testing.T) {
	setValidEnv(t)
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Redis.Addr != "" || cfg.Limits.Attempts != 5 || cfg.Limits.Window != 15*time.Minute ||
		cfg.SMTP.Enabled() || cfg.SMTP.Port != 587 || cfg.SMTP.Security != "starttls" ||
		cfg.Password.ResetTTL != 30*time.Minute || cfg.Password.ResetURL != "" {
		t.Errorf("defaults: %+v %+v %+v %+v", cfg.Redis, cfg.Limits, cfg.SMTP, cfg.Password)
	}
}

func TestServiceSettings_Invalid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"redis without port", map[string]string{"REDIS_ADDR": "localhost"}, "REDIS_ADDR"},
		{"zero attempts", map[string]string{"RATE_LIMIT_ATTEMPTS": "0"}, "RATE_LIMIT_ATTEMPTS"},
		{"tiny window", map[string]string{"RATE_LIMIT_WINDOW": "100ms"}, "RATE_LIMIT_WINDOW"},
		{"reset ttl too short", map[string]string{"PASSWORD_RESET_TTL": "1m"}, "PASSWORD_RESET_TTL"},
		{"reset ttl too long", map[string]string{"PASSWORD_RESET_TTL": "72h"}, "PASSWORD_RESET_TTL"},
		{"reset url not http", map[string]string{"PASSWORD_RESET_URL": "javascript:alert(1)"}, "PASSWORD_RESET_URL"},
		{"smtp without from", map[string]string{"SMTP_HOST": "smtp.example.com"}, "SMTP_FROM"},
		{"smtp bad security", map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "a@b.test", "SMTP_SECURITY": "ssl"}, "SMTP_SECURITY"},
		{"smtp bad port", map[string]string{"SMTP_HOST": "h", "SMTP_FROM": "a@b.test", "SMTP_PORT": "70000"}, "SMTP_PORT"},
		{"production plain smtp", map[string]string{"APP_ENV": "production", "DB_PASSWORD": "x", "DB_SSLMODE": "require", "SMTP_HOST": "h", "SMTP_FROM": "a@b.test", "SMTP_SECURITY": "none"}, "SMTP_SECURITY=none"},
		{"production http reset url", map[string]string{"APP_ENV": "production", "DB_PASSWORD": "x", "DB_SSLMODE": "require", "PASSWORD_RESET_URL": "http://app.example.com/r?t={token}"}, "https in production"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := fromEnv()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	setValidEnv(t)
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "App <no-reply@example.com>")
	t.Setenv("PASSWORD_RESET_URL", "https://{tenant}.example.com/reset?token={token}")
	t.Setenv("REDIS_ADDR", "redis:6379")
	if _, err := fromEnv(); err != nil {
		t.Fatalf("a valid setup must load: %v", err)
	}
}

func TestGRPCSettings(t *testing.T) {
	setValidEnv(t)
	cfg, err := fromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPC.Addr != ":9090" || cfg.GRPC.TLSCertFile != "" || cfg.GRPC.AllowPlaintext {
		t.Errorf("defaults: %+v", cfg.GRPC)
	}
	if err := cfg.ValidateGRPC(); err != nil {
		t.Errorf("development without TLS must be valid: %v", err)
	}
	// the REST server does not care about gRPC settings, even in production
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_PASSWORD", "x")
	t.Setenv("DB_SSLMODE", "require")
	prod, err := fromEnv()
	if err != nil {
		t.Fatalf("production REST config must not require gRPC TLS: %v", err)
	}
	if err := prod.ValidateGRPC(); err == nil || !strings.Contains(err.Error(), "GRPC_TLS_CERT_FILE") {
		t.Errorf("production gRPC without TLS must be refused: %v", err)
	}
}

func TestValidateGRPC(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // "" = valid
	}{
		{"development plain", nil, ""},
		{"bad address", map[string]string{"GRPC_ADDR": "9090"}, "GRPC_ADDR"},
		{"cert without key", map[string]string{"GRPC_TLS_CERT_FILE": "c.pem"}, "together"},
		{"key without cert", map[string]string{"GRPC_TLS_KEY_FILE": "k.pem"}, "together"},
		{"client CA without server cert", map[string]string{"GRPC_TLS_CLIENT_CA_FILE": "ca.pem"}, "GRPC_TLS_CLIENT_CA_FILE"},
		{"plaintext and cert", map[string]string{"GRPC_ALLOW_PLAINTEXT": "true", "GRPC_TLS_CERT_FILE": "c", "GRPC_TLS_KEY_FILE": "k"}, "contradicts"},
		{"production plain", map[string]string{"APP_ENV": "production", "DB_PASSWORD": "x", "DB_SSLMODE": "require"}, "production needs"},
		{"production TLS", map[string]string{"APP_ENV": "production", "DB_PASSWORD": "x", "DB_SSLMODE": "require", "GRPC_TLS_CERT_FILE": "c", "GRPC_TLS_KEY_FILE": "k"}, ""},
		{"production mTLS", map[string]string{"APP_ENV": "production", "DB_PASSWORD": "x", "DB_SSLMODE": "require", "GRPC_TLS_CERT_FILE": "c", "GRPC_TLS_KEY_FILE": "k", "GRPC_TLS_CLIENT_CA_FILE": "ca"}, ""},
		{"production behind a mesh", map[string]string{"APP_ENV": "production", "DB_PASSWORD": "x", "DB_SSLMODE": "require", "GRPC_ALLOW_PLAINTEXT": "true"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setValidEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg, err := fromEnv()
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateGRPC()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	setValidEnv(t)
	t.Setenv("GRPC_ALLOW_PLAINTEXT", "maybe")
	if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "GRPC_ALLOW_PLAINTEXT") {
		t.Errorf("a non-boolean must be refused: %v", err)
	}
}

func TestTenantDB(t *testing.T) {
	setValidEnv(t)
	cfg, err := fromEnv()
	if err != nil || len(cfg.TenantDB.URLs) != 0 || cfg.TenantDB.MaxOpenConns != 10 {
		t.Fatalf("default: %+v %v", cfg, err)
	}

	t.Setenv("TENANT_DB_URL_ACME", "postgres://u:p@h:5432/acme?sslmode=disable")
	cfg, err = fromEnv()
	if err != nil || cfg.TenantDB.URLs["acme"] == "" {
		t.Fatalf("a reference is the lower case suffix: %+v %v", cfg.TenantDB, err)
	}

	bad := map[string]string{
		"TENANT_DB_URL_ACME": "mysql://u@h/db",
	}
	for k, v := range bad {
		t.Run(k+"="+v, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv(k, v)
			if _, err := fromEnv(); err == nil {
				t.Fatal("must be rejected")
			}
		})
	}

	// production wants TLS to the dedicated database too
	setValidEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("TENANT_DB_URL_ACME", "postgres://u:p@h:5432/acme?sslmode=disable")
	if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "TENANT_DB_URL_ACME") {
		t.Fatalf("production must reject sslmode=disable: %v", err)
	}
	t.Setenv("TENANT_DB_URL_ACME", "postgres://u:p@h:5432/acme?sslmode=verify-full")
	if _, err := fromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsConfig(t *testing.T) {
	setValidEnv(t)
	cfg, err := fromEnv()
	if err != nil || !cfg.Metrics.Enabled {
		t.Fatalf("development: metrics are on by default: %+v %v", cfg.Metrics, err)
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_SSLMODE", "require")
	cfg, err = fromEnv()
	if err != nil || cfg.Metrics.Enabled {
		t.Fatalf("production: off by default: %+v %v", cfg.Metrics, err)
	}
	t.Setenv("METRICS_ENABLED", "true")
	if _, err := fromEnv(); err == nil || !strings.Contains(err.Error(), "METRICS_TOKEN") {
		t.Fatalf("production with metrics needs a token: %v", err)
	}
	t.Setenv("METRICS_TOKEN", "0123456789abcdef")
	if _, err := fromEnv(); err != nil {
		t.Fatal(err)
	}
}

func TestDocsConfig(t *testing.T) {
	setValidEnv(t)
	if cfg, err := fromEnv(); err != nil || !cfg.Docs.Enabled {
		t.Fatalf("development: on by default: %+v %v", cfg.Docs, err)
	}
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_PASSWORD", "secret")
	t.Setenv("DB_SSLMODE", "require")
	if cfg, err := fromEnv(); err != nil || cfg.Docs.Enabled {
		t.Fatalf("production: off by default: %+v %v", cfg.Docs, err)
	}
	t.Setenv("DOCS_ENABLED", "true")
	if cfg, err := fromEnv(); err != nil || !cfg.Docs.Enabled {
		t.Fatalf("production: can be switched on: %+v %v", cfg.Docs, err)
	}
}
