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
