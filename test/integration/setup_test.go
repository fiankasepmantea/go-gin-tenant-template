//go:build integration

// Package integration tests repositories against a real PostgreSQL, including RLS.
//
// Run:  make docker-up && make test-integration
// Uses TEST_DATABASE_URL (owner/superuser role). The tests create a temporary database, apply
// migrations/*.up.sql, then connect the application as a regular role without BYPASSRLS.
package integration

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

const appRole = "tenant_app_it"

type env struct {
	owner *gorm.DB // table owner: BYPASSES RLS, used only to set up data and to prove the application layer
	app   *gorm.DB // application role: subject to RLS
}

func open(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func withDB(raw *url.URL, name, user, pass string) string {
	u := *raw
	u.Path = "/" + name
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u.String()
}

// testDSN returns TEST_DATABASE_URL, or builds one from the DB_* variables of .env.<mode>
// (the same values docker compose uses), so `make docker-up` and the tests always agree.
func testDSN() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	user := os.Getenv("DB_USER")
	if user == "" {
		return ""
	}
	host, port := os.Getenv("DB_HOST"), os.Getenv("DB_PORT")
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "5432"
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, os.Getenv("DB_PASSWORD")),
		Host:     net.JoinHostPort(host, port),
		Path:     "/postgres",
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func setup(t *testing.T) *env {
	t.Helper()
	dsn := testDSN()
	if dsn == "" {
		t.Skip("neither TEST_DATABASE_URL nor DB_USER/DB_PASSWORD is set")
	}
	raw, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}

	admin := open(t, dsn)
	dbName := "tenant_it_" + idgen.NewUUID().String()[24:]
	if err := admin.Exec(`CREATE DATABASE "` + dbName + `"`).Error; err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_ = admin.Exec(`DROP DATABASE IF EXISTS "` + dbName + `" WITH (FORCE)`).Error
	})

	// application role: not a superuser, no BYPASSRLS. Created once, reused.
	if err := admin.Exec(fmt.Sprintf(`DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
			CREATE ROLE %s LOGIN PASSWORD 'it' NOSUPERUSER NOBYPASSRLS;
		END IF; END $$`, appRole, appRole)).Error; err != nil {
		t.Fatalf("create role: %v", err)
	}

	ownerDB := open(t, withDB(raw, dbName, "", ""))
	files, _ := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := ownerDB.Exec(string(sql)).Error; err != nil {
			t.Fatalf("migration %s: %v", filepath.Base(f), err)
		}
	}
	for _, q := range []string{
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO " + appRole,
		"GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO " + appRole,
	} {
		if err := ownerDB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}

	return &env{owner: ownerDB, app: open(t, withDB(raw, dbName, appRole, "it"))}
}

// newTenant creates a tenant through the owner connection and returns a ctx carrying that tenant.
func (e *env) newTenant(t *testing.T, code string) (id string, ctx context.Context) {
	t.Helper()
	id = idgen.New()
	err := e.owner.Exec("INSERT INTO tenants (tenant_id, code, name) VALUES (?, ?, ?)", id, code, code).Error
	if err != nil {
		t.Fatal(err)
	}
	return id, tenant.WithID(context.Background(), id)
}
