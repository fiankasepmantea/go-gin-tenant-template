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

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/session"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/logger"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/rest/middleware"
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
	sessions, err := session.NewService(session.Deps{
		Tx:         session.NewTxRunner(db),
		Tenants:    tenant.NewRepository(db),
		Users:      user.NewRepository(db),
		Tokens:     session.NewRepository(db),
		Hasher:     hasher,
		Issuer:     tokens,
		AccessTTL:  cfg.Auth.AccessTTL,
		RefreshTTL: cfg.Auth.RefreshTTL,
		Log:        log,
	})
	if err != nil {
		return err
	}
	// How the tenant is found at login: subdomain first (web), then header (mobile/API), then the request body.
	resolver := tenantctx.Chain(
		tenantctx.SubdomainResolver{BaseDomain: cfg.Tenant.BaseDomain},
		tenantctx.HeaderResolver{Name: cfg.Tenant.Header},
	)
	authHandler := session.NewHandler(sessions, resolver, middleware.Authenticate(tokens), log)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           rest.NewRouter(cfg, log, db, authHandler),
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
	return srv.Shutdown(shutdownCtx)
}
