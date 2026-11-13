// Package database opens PostgreSQL connections through GORM.
package database

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
)

// DSN builds a URL-form connection string so that special characters
// in the password (space, @, :, etc.) are escaped automatically.
func DSN(c config.Database) string {
	q := url.Values{}
	q.Set("sslmode", c.SSLMode)
	q.Set("TimeZone", "UTC")
	if c.Schema != "" {
		q.Set("search_path", c.Schema)
	}

	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.User, c.Password),
		Host:     net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:     "/" + c.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

// Pool sets the size of a connection pool.
type Pool struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	Quiet       bool // log only warnings and errors (the CLI: a command should print its result, not its SQL)
}

// Connect opens a connection, configures the connection pool, and verifies the database is reachable.
func Connect(cfg config.Database, production bool) (*gorm.DB, error) {
	return ConnectURL(DSN(cfg), Pool{MaxOpen: cfg.MaxOpenConns, MaxIdle: cfg.MaxIdleConns, MaxLifetime: cfg.ConnMaxLifetime}, production)
}

// ConnectURL is Connect for a ready connection URL (a dedicated tenant database, see Router).
func ConnectURL(dsn string, pool Pool, production bool) (*gorm.DB, error) {
	level := logger.Info
	if production || pool.Quiet {
		level = logger.Warn
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger:  logger.Default.LogMode(level),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("database: failed to open connection: %w", redact(err, dsn))
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	sqlDB.SetMaxOpenConns(pool.MaxOpen)
	sqlDB.SetMaxIdleConns(pool.MaxIdle)
	sqlDB.SetConnMaxLifetime(pool.MaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("database: ping failed: %w", redact(err, dsn))
	}
	return db, nil
}

// redact keeps the password of a connection URL out of an error message (a log line, a terminal).
func redact(err error, dsn string) error {
	u, perr := url.Parse(dsn)
	if perr != nil || u.User == nil {
		return err
	}
	pw, ok := u.User.Password()
	if !ok || pw == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), pw, "***"))
}
