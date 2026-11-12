package database

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
)

func fakeRouter(urls map[string]string, open func(string) (*gorm.DB, error)) *Router {
	r := NewRouter(RouterConfig{URLs: urls, RetryAfter: 20 * time.Millisecond})
	r.open = open
	return r
}

func TestRouter_DedicatedOpensOnceAndCaches(t *testing.T) {
	opened := 0
	r := fakeRouter(map[string]string{"acme": "postgres://x"}, func(string) (*gorm.DB, error) { opened++; return &gorm.DB{}, nil })
	a, err := r.Dedicated("acme")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Dedicated("acme")
	if a != b || opened != 1 {
		t.Fatalf("the pool must be opened once: opened=%d", opened)
	}
}

func TestRouter_UnconfiguredRefIsUnavailable(t *testing.T) {
	r := fakeRouter(map[string]string{"acme": "x"}, nil)
	_, err := r.Dedicated("other")
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrTenantDatabaseUnavailable) {
		t.Fatalf("got %v", err)
	}
	if r.Configured("other") || !r.Configured("acme") {
		t.Fatal("Configured")
	}
}

func TestRouter_FailedOpenBacksOffThenRetries(t *testing.T) {
	calls := 0
	fail := true
	r := fakeRouter(map[string]string{"acme": "x"}, func(string) (*gorm.DB, error) {
		calls++
		if fail {
			return nil, errors.New("refused")
		}
		return &gorm.DB{}, nil
	})
	for i := 0; i < 5; i++ {
		if _, err := r.Dedicated("acme"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("got %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("a failed open must not be retried at once: %d calls", calls)
	}
	fail = false
	time.Sleep(30 * time.Millisecond)
	if _, err := r.Dedicated("acme"); err != nil {
		t.Fatalf("after the back-off the database is tried again: %v", err)
	}
}

func TestRouter_ClosedRefusesAndRefsAreSorted(t *testing.T) {
	r := fakeRouter(map[string]string{"b": "x", "a": "x"}, func(string) (*gorm.DB, error) { return &gorm.DB{}, nil })
	if got := fmt.Sprint(r.Refs()); got != "[a b]" {
		t.Fatalf("refs: %s", got)
	}
	r.Close()
	if _, err := r.Dedicated("a"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("after Close: %v", err)
	}
}

func TestRouter_TenantLevelNeedsTenant(t *testing.T) {
	r := fakeRouter(nil, nil)
	err := r.InTenantTx(context.Background(), auth.LevelTenant, func(context.Context) error { return nil })
	if !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("got %v", err)
	}
}

func TestIsConnectionError(t *testing.T) {
	yes := []error{
		&pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "57P01"}, driver.ErrBadConn, io.ErrUnexpectedEOF,
		fmt.Errorf("wrapped: %w", driver.ErrBadConn),
	}
	for _, e := range yes {
		if !isConnectionError(e) {
			t.Errorf("%v must be a connection error", e)
		}
	}
	no := []error{nil, errors.New("boom"), &pgconn.PgError{Code: "23505"}, &pgconn.PgError{Code: "42P01"}}
	for _, e := range no {
		if isConnectionError(e) {
			t.Errorf("%v must not be a connection error", e)
		}
	}
}
