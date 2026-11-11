package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"time"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	authv1 "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc/gen/tenantapp/auth/v1"
)

// Server limits. A request carries one token and one route; 16 KiB is far more than it needs.
const (
	maxRecvBytes   = 16 << 10
	callTimeout    = 5 * time.Second
	maxConcurrent  = 1000
	connectTimeout = 5 * time.Second
)

// Options configure NewServer.
type Options struct {
	Auth       *AuthServer
	Log        *slog.Logger
	TLS        *tls.Config // nil: plaintext (development, or a service mesh that encrypts)
	Reflection bool        // lets grpcurl list the services; development only
}

// Server wraps grpc.Server with the template's interceptors and the standard health service.
type Server struct {
	srv    *grpclib.Server
	health *health.Server
	log    *slog.Logger
}

// NewServer creates the gRPC server with AuthService registered.
func NewServer(o Options) (*Server, error) {
	if o.Auth == nil {
		return nil, errors.New("grpc: Auth is required")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	opts := []grpclib.ServerOption{
		grpclib.ChainUnaryInterceptor(recoverInterceptor(o.Log), logInterceptor(o.Log), timeoutInterceptor(callTimeout)),
		grpclib.MaxRecvMsgSize(maxRecvBytes),
		grpclib.MaxConcurrentStreams(maxConcurrent),
		grpclib.ConnectionTimeout(connectTimeout),
		grpclib.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 20 * time.Second}),
	}
	if o.TLS != nil {
		opts = append(opts, grpclib.Creds(credentials.NewTLS(o.TLS)))
	}
	srv := grpclib.NewServer(opts...)
	authv1.RegisterAuthServiceServer(srv, o.Auth)

	h := health.NewServer()
	healthpb.RegisterHealthServer(srv, h)
	h.SetServingStatus(authv1.AuthService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)
	if o.Reflection {
		reflection.Register(srv)
	}
	return &Server{srv: srv, health: h, log: o.Log}, nil
}

// Serve accepts connections on lis until Shutdown is called. It returns nil after a normal shutdown.
func (s *Server) Serve(lis net.Listener) error {
	if err := s.srv.Serve(lis); err != nil && !errors.Is(err, grpclib.ErrServerStopped) {
		return err
	}
	return nil
}

// Shutdown stops accepting calls and waits for the running ones; after grace it cuts the connections.
func (s *Server) Shutdown(grace time.Duration) {
	s.health.Shutdown() // load balancers stop sending new calls
	done := make(chan struct{})
	go func() { s.srv.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		s.srv.Stop()
	}
}

// TLSConfig loads the server certificate and, when clientCAFile is set, requires every client to present a
// certificate signed by that CA (mutual TLS: only your own services can call the AuthService).
func TLSConfig(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("grpc: load server certificate: %w", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if clientCAFile != "" {
		pem, err := os.ReadFile(clientCAFile) //nolint:gosec // path from the operator's configuration
		if err != nil {
			return nil, fmt.Errorf("grpc: read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("grpc: GRPC_TLS_CLIENT_CA_FILE has no PEM certificate")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// recoverInterceptor turns a panic into codes.Internal; a caller must never get "allowed" from a crash.
func recoverInterceptor(log *slog.Logger) grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpclib.UnaryServerInfo, h grpclib.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.LogAttrs(ctx, slog.LevelError, "grpc_panic",
					slog.String("method", info.FullMethod), slog.Any("panic", r), slog.String("stack", string(debug.Stack())))
				resp, err = nil, status.Error(codes.Internal, "internal error")
			}
		}()
		return h(ctx, req)
	}
}

// logInterceptor writes one line per call: method, result and duration. The request is never logged (it holds
// an access token).
func logInterceptor(log *slog.Logger) grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpclib.UnaryServerInfo, h grpclib.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := h(ctx, req)
		code := status.Code(err)
		lvl := slog.LevelInfo
		if code == codes.Internal || code == codes.Unknown {
			lvl = slog.LevelError
		}
		log.LogAttrs(ctx, lvl, "grpc_call",
			slog.String("method", info.FullMethod),
			slog.String("code", code.String()),
			slog.Duration("took", time.Since(start)),
		)
		return resp, err
	}
}

// timeoutInterceptor bounds every call, so a slow database cannot pile up requests.
func timeoutInterceptor(d time.Duration) grpclib.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpclib.UnaryServerInfo, h grpclib.UnaryHandler) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return h(ctx, req)
	}
}
