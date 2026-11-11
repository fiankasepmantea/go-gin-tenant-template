package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/app"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/logger"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/mailer"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ratelimit"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log, err := logger.New(os.Stdout, cfg.LogLevel, cfg.IsProduction())
	if err != nil {
		return err
	}
	slog.SetDefault(log)

	db, err := database.Connect(cfg.DB, cfg.IsProduction())
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	tokens, err := auth.NewManager(cfg.Auth.PasetoKeyHex, cfg.Auth.Issuer, cfg.Auth.AccessTTL)
	if err != nil {
		return err
	}
	hasher, err := password.New(password.DefaultParams())
	if err != nil {
		return err
	}
	limits := session.Limits{Attempts: cfg.Limits.Attempts, Window: cfg.Limits.Window}
	limiter, closeLimiter := newLimiter(cfg, log)
	defer closeLimiter()

	sessions, err := session.NewService(session.Deps{
		Tx:         session.NewTxRunner(db),
		Tenants:    tenant.NewRepository(db),
		Users:      user.NewRepository(db),
		Tokens:     session.NewRepository(db),
		Hasher:     hasher,
		Issuer:     tokens,
		AccessTTL:  cfg.Auth.AccessTTL,
		RefreshTTL: cfg.Auth.RefreshTTL,
		Limiter:    limiter,
		Limits:     limits,
		Log:        log,
	})
	if err != nil {
		return err
	}
	account, err := newAccount(cfg, log, db, hasher, limiter, limits)
	if err != nil {
		return err
	}
	// How the tenant is found at login: subdomain first (web), then header (mobile/API), then the request body.
	resolver := tenantctx.Chain(
		tenantctx.SubdomainResolver{BaseDomain: cfg.Tenant.BaseDomain},
		tenantctx.HeaderResolver{Name: cfg.Tenant.Header},
	)
	modules := app.Modules(app.Deps{Log: log, DB: db, Tokens: tokens, Sessions: sessions, Account: account, Resolver: resolver})

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           rest.NewRouter(cfg, log, db, modules...),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("server running", "addr", srv.Addr, "env", cfg.Env, "version", version.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	// Graceful shutdown: wait for in-flight requests to finish.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	account.Wait() // let the e-mails that are being sent finish
	return err
}

// newLimiter creates the rate limiter: Redis when REDIS_ADDR is set, otherwise memory (one replica only).
// Redis being down is not fatal: the limiter then lets requests through and logs the error.
func newLimiter(cfg *config.Config, log *slog.Logger) (*ratelimit.Limiter, func()) {
	if cfg.Redis.Addr == "" {
		if cfg.IsProduction() {
			log.Warn("REDIS_ADDR is not set: login rate limits live in this process only (correct for one replica, weak for several)")
		}
		return ratelimit.New(ratelimit.NewMemoryStore(nil), log), func() {}
	}
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB,
		DialTimeout: time.Second, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond,
		MaxRetries: 1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Error("cannot reach Redis: rate limiting is off until it is back", "addr", cfg.Redis.Addr, "error", err)
	}
	return ratelimit.New(ratelimit.NewRedisStore(rdb), log), func() { _ = rdb.Close() }
}

// newAccount wires password change and reset. Without SMTP the message is only logged, and the log
// contains the reset link only outside production.
func newAccount(cfg *config.Config, log *slog.Logger, db *gorm.DB, hasher *password.Hasher, limiter *ratelimit.Limiter, limits session.Limits) (*session.Account, error) {
	var m mailer.Mailer
	if cfg.SMTP.Enabled() {
		smtp, err := mailer.NewSMTP(mailer.SMTPConfig{
			Host: cfg.SMTP.Host, Port: cfg.SMTP.Port, Username: cfg.SMTP.Username, Password: cfg.SMTP.Password,
			From: cfg.SMTP.From, Security: mailer.Security(cfg.SMTP.Security),
		})
		if err != nil {
			return nil, err
		}
		m = smtp
	} else {
		if cfg.IsProduction() {
			log.Warn("SMTP_HOST is not set: password reset e-mails cannot be delivered")
		}
		m = mailer.LogMailer{Log: log, ShowBody: !cfg.IsProduction()}
	}
	notifier, err := session.NewMailNotifier(m, cfg.Password.AppName, cfg.Password.ResetURL)
	if err != nil {
		return nil, err
	}
	return session.NewAccount(session.AccountDeps{
		Tx:       session.NewTxRunner(db),
		Tenants:  tenant.NewRepository(db),
		Users:    user.NewRepository(db),
		Tokens:   session.NewRepository(db),
		Resets:   session.NewResetRepository(db),
		Hasher:   hasher,
		Notifier: notifier,
		Limiter:  limiter,
		Limits:   limits,
		ResetTTL: cfg.Password.ResetTTL,
		Log:      log,
	})
}
