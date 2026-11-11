package grpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
	authv1 "github.com/fiankasepmantea/go-gin-tenant-template/internal/transport/grpc/gen/tenantapp/auth/v1"
)

// TokenParser verifies an access token. *auth.Manager implements it.
type TokenParser interface {
	Parse(token string) (*auth.Claims, error)
}

// Authorizer decides whether a caller may use an endpoint. *rbac.Service implements it; it is the same
// interface the REST middleware uses.
type Authorizer interface {
	Authorize(ctx context.Context, claims *auth.Claims, method, route string) (auth.Decision, error)
}

// TxFunc runs fn inside a transaction that has the row level security variables of the caller set
// (database.InTenantTx). Without it a tenant query sees no rows at all.
type TxFunc func(ctx context.Context, level string, fn func(ctx context.Context) error) error

// DBTx is the TxFunc for a real database.
func DBTx(db *gorm.DB) TxFunc {
	return func(ctx context.Context, level string, fn func(ctx context.Context) error) error {
		return database.InTenantTx(ctx, db, level, fn)
	}
}

// Limits of the input. A token is a few hundred bytes and a route pattern is short; anything bigger is not ours.
const (
	maxTokenLen = 4096
	maxPathLen  = 512
)

var validMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true}

// AuthServer implements authv1.AuthServiceServer.
type AuthServer struct {
	authv1.UnimplementedAuthServiceServer
	tokens TokenParser
	az     Authorizer
	tx     TxFunc
	log    *slog.Logger
}

// NewAuthServer creates the service. All arguments except log are required.
func NewAuthServer(tokens TokenParser, az Authorizer, tx TxFunc, log *slog.Logger) (*AuthServer, error) {
	if tokens == nil || az == nil || tx == nil {
		return nil, errors.New("grpc: tokens, authorizer and tx are required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AuthServer{tokens: tokens, az: az, tx: tx, log: log}, nil
}

// claimsOf verifies the token. Every failure is the same answer: the caller learns nothing about why.
func (s *AuthServer) claimsOf(token string) (*auth.Claims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, status.Error(codes.InvalidArgument, "access_token is required")
	}
	if len(token) > maxTokenLen {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	claims, err := s.tokens.Parse(token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
	return claims, nil
}

// Validate checks the token and returns who owns it. Like the REST Authenticate middleware it only verifies the
// token (signature, expiry, issuer); an access token stays valid until it expires, even after logout, so keep
// ACCESS_TOKEN_TTL short. Use Authorize to also check that the user still exists and is active.
func (s *AuthServer) Validate(_ context.Context, req *authv1.ValidateRequest) (*authv1.ValidateResponse, error) {
	claims, err := s.claimsOf(req.GetAccessToken())
	if err != nil {
		return nil, err
	}
	return &authv1.ValidateResponse{UserId: claims.UserID, TenantId: claims.TenantID, Level: claims.Level}, nil
}

// Authorize answers whether the token's owner may call method on the route pattern path in their tenant.
//
//	allowed=true   the group of the user was granted the endpoint (or it is public / the caller is a platform admin)
//	allowed=false  refused (also for an endpoint that is not registered: default deny)
//	Unauthenticated   bad token, or the user no longer exists or is inactive
//	Internal          the decision could not be made; the caller must treat it as "no" (fail closed)
func (s *AuthServer) Authorize(ctx context.Context, req *authv1.AuthorizeRequest) (*authv1.AuthorizeResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(req.GetMethod()))
	path := strings.TrimSpace(req.GetPath())
	if !validMethods[method] {
		return nil, status.Error(codes.InvalidArgument, "method must be one of GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS")
	}
	if path == "" || path[0] != '/' || len(path) > maxPathLen {
		return nil, status.Error(codes.InvalidArgument, "path must be a route pattern that starts with /, for example /api/v1/groups/:id")
	}
	claims, err := s.claimsOf(req.GetAccessToken())
	if err != nil {
		return nil, err
	}

	// the tenant always comes from the verified token, never from the request
	if claims.Level == auth.LevelTenant {
		ctx = tenant.WithID(ctx, claims.TenantID)
	}
	var d auth.Decision
	err = s.tx(ctx, claims.Level, func(ctx context.Context) error {
		var derr error
		d, derr = s.az.Authorize(ctx, claims, method, path)
		return derr
	})
	if err != nil {
		s.log.LogAttrs(ctx, slog.LevelError, "grpc_authorize_failed",
			slog.String("user_id", claims.UserID), slog.String("tenant_id", claims.TenantID), slog.String("error", err.Error()))
		return nil, status.Error(codes.Internal, "the decision could not be made")
	}

	switch d.Verdict {
	case auth.VerdictAllowed:
		return &authv1.AuthorizeResponse{Allowed: true}, nil
	case auth.VerdictUnauthenticated:
		s.logDenied(ctx, claims, method, path, d)
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	default:
		s.logDenied(ctx, claims, method, path, d)
		return &authv1.AuthorizeResponse{Allowed: false}, nil
	}
}

// logDenied writes the reason of a refusal to the server log; the caller only gets "no".
func (s *AuthServer) logDenied(ctx context.Context, claims *auth.Claims, method, path string, d auth.Decision) {
	s.log.LogAttrs(ctx, slog.LevelWarn, "access_denied",
		slog.String("transport", "grpc"),
		slog.String("user_id", claims.UserID),
		slog.String("tenant_id", claims.TenantID),
		slog.String("level", claims.Level),
		slog.String("method", method),
		slog.String("route", path),
		slog.String("reason", d.Reason),
	)
}
