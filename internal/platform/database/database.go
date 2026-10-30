// Package database opens PostgreSQL connections through GORM.
package database

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
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

// Connect opens a connection, configures the connection pool, and verifies the database is reachable.
func Connect(cfg config.Database, production bool) (*gorm.DB, error) {
	level := logger.Info
	if production {
		level = logger.Warn
	}

	db, err := gorm.Open(postgres.Open(DSN(cfg)), &gorm.Config{
		Logger:  logger.Default.LogMode(level),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("database: failed to open connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("database: ping failed: %w", err)
	}
	return db, nil
}
