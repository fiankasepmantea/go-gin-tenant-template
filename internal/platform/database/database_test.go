package database

import (
	"net/url"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
)

func TestDSN(t *testing.T) {
	dsn := DSN(config.Database{
		Host:     "db.local",
		Port:     5432,
		User:     "app user",
		Password: "p@ss:w/rd #1",
		Name:     "tenantdb",
		Schema:   "core",
		SSLMode:  "require",
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN is not a valid URL: %v (%s)", err, dsn)
	}
	if u.Host != "db.local:5432" || u.Path != "/tenantdb" {
		t.Errorf("wrong host/path: %s", dsn)
	}
	if u.User.Username() != "app user" {
		t.Errorf("user = %q", u.User.Username())
	}
	if pw, _ := u.User.Password(); pw != "p@ss:w/rd #1" {
		t.Errorf("password did not round-trip intact: %q", pw)
	}
	q := u.Query()
	if q.Get("sslmode") != "require" || q.Get("search_path") != "core" || q.Get("TimeZone") != "UTC" {
		t.Errorf("wrong query: %v", q)
	}
}

func TestDSN_NoSchema(t *testing.T) {
	dsn := DSN(config.Database{Host: "h", Port: 1, User: "u", Name: "d", SSLMode: "disable"})
	u, _ := url.Parse(dsn)
	if u.Query().Has("search_path") {
		t.Errorf("an empty schema must not add search_path: %s", dsn)
	}
}
