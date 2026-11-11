// Command grpc runs the gRPC AuthService (api/proto/tenantapp/auth/v1): other services call it to verify access
// tokens and to ask whether a user may use an endpoint. It needs the database and the PASETO key, nothing else.
// main only loads the configuration, wires the dependencies and runs the server (see docs/grpc.md).
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/rbac"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/logger"
	grpcsrv "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc"
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
	if err := cfg.ValidateGRPC(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
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
	// The same decision maker as the REST middleware. Its caches follow changes made in another process (the REST
	// server, the CLI) after at most one TTL: 60s for endpoints, 30s for users and grants.
	az := rbac.NewService(endpoint.NewRepository(db), rbac.NewRepository(db), rbac.Options{})
	authSrv, err := grpcsrv.NewAuthServer(tokens, az, grpcsrv.DBTx(db), log)
	if err != nil {
		return err
	}

	var tlsCfg *tls.Config
	if cfg.GRPC.TLSCertFile != "" {
		if tlsCfg, err = grpcsrv.TLSConfig(cfg.GRPC.TLSCertFile, cfg.GRPC.TLSKeyFile, cfg.GRPC.ClientCAFile); err != nil {
			return err
		}
	} else {
		log.Warn("gRPC without TLS: only for development or behind a service mesh that encrypts")
	}
	srv, err := grpcsrv.NewServer(grpcsrv.Options{Auth: authSrv, Log: log, TLS: tlsCfg, Reflection: !cfg.IsProduction()})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.GRPC.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", cfg.GRPC.Addr, err)
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("gRPC server running", "addr", lis.Addr().String(), "tls", tlsCfg != nil, "mtls", cfg.GRPC.ClientCAFile != "",
			"env", cfg.Env, "version", version.Version)
		serveErr <- srv.Serve(lis)
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down gRPC server")
	srv.Shutdown(10 * time.Second)
	return <-serveErr
}
