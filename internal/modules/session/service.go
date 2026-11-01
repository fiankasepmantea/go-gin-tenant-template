// Package session is the authentication module: login, refresh-token rotation and logout.
//
// Login identifies the tenant (the "unit"), checks the password and issues a short-lived PASETO access token
// plus an opaque refresh token. After login only the token is trusted; see docs/authentication.md.
package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

var (
	// ErrInvalidCredentials is returned for every failed login. It deliberately does not say whether the tenant,
	// the username or the password was wrong, or whether the tenant is suspended or the user inactive.
	ErrInvalidCredentials = errors.New("session: invalid credentials")
	// ErrInvalidRefreshToken is returned when a refresh token is unknown, expired, revoked or malformed.
	ErrInvalidRefreshToken = errors.New("session: invalid refresh token")
)

// scopePlatform is the prefix of a platform admin's refresh token. Tenant sessions use the tenant_id instead,
// so a refresh token tells the server which tenant to look in before any database access (RLS needs it).
const scopePlatform = "platform"

// Narrow interfaces keep the service testable without a database.

type TokenStore interface {
	Create(ctx context.Context, t *UserToken) error
	FindByHash(ctx context.Context, hash []byte) (*UserToken, error)
	Revoke(ctx context.Context, id string, at time.Time) (bool, error)
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) error
}

type TenantFinder interface {
	FindByCode(ctx context.Context, code string) (*tenant.Tenant, error)
}

type UserStore interface {
	FindByUsername(ctx context.Context, username string) (*user.User, error)
	FindPlatformByUsername(ctx context.Context, username string) (*user.User, error)
	GetByID(ctx context.Context, id string) (*user.User, error)
	FindPlatformByID(ctx context.Context, id string) (*user.User, error)
	SetPasswordHash(ctx context.Context, u *user.User, hash string) error
}

// AccessIssuer creates access tokens. *auth.Manager implements it.
type AccessIssuer interface {
	Issue(c auth.Claims) (string, error)
}

// TxRunner runs fn in a transaction with the RLS session variables set for the given level.
// Production uses database.InTenantTx; see NewTxRunner.
type TxRunner func(ctx context.Context, level string, fn func(ctx context.Context) error) error

// Deps are the collaborators of Service.
type Deps struct {
	Tx         TxRunner
	Tenants    TenantFinder
	Users      UserStore
	Tokens     TokenStore
	Hasher     *password.Hasher
	Issuer     AccessIssuer
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Log        *slog.Logger
	Now        func() time.Time // nil = time.Now
}

// Service implements login, refresh and logout.
type Service struct {
	d Deps
}

// NewService validates the dependencies and creates the Service.
func NewService(d Deps) (*Service, error) {
	switch {
	case d.Tx == nil, d.Tenants == nil, d.Users == nil, d.Tokens == nil, d.Hasher == nil, d.Issuer == nil:
		return nil, errors.New("session: missing dependency")
	case d.AccessTTL <= 0 || d.RefreshTTL <= d.AccessTTL:
		return nil, errors.New("session: RefreshTTL must be greater than AccessTTL, and both positive")
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{d: d}, nil
}

// LoginInput is a login attempt.
type LoginInput struct {
	TenantCode string // ignored by LoginPlatform
	Username   string
	Password   string
	UserAgent  string
	IP         string
}

// Tokens is the result of a successful login or refresh.
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration // lifetime of AccessToken
}

// Login signs a user in to a tenant.
func (s *Service) Login(ctx context.Context, in LoginInput) (*Tokens, error) {
	code, ok := tenantctx.NormalizeCode(in.TenantCode)
	if !ok {
		s.d.Hasher.Dummy(in.Password)
		return nil, ErrInvalidCredentials
	}

	t, err := s.d.Tenants.FindByCode(ctx, code)
	switch {
	case errors.Is(err, core.ErrNotFound):
		s.d.Hasher.Dummy(in.Password)
		return nil, ErrInvalidCredentials
	case err != nil:
		return nil, fmt.Errorf("session: find tenant: %w", err)
	case t.Status != tenant.StatusActive:
		s.d.Log.Info("login rejected: tenant is not active", "tenant_id", t.ID, "status", t.Status)
		s.d.Hasher.Dummy(in.Password)
		return nil, ErrInvalidCredentials
	}

	return s.login(tenantctx.WithID(ctx, t.ID), auth.LevelTenant, t.ID, in)
}

// LoginPlatform signs a platform admin in. Platform admins belong to no tenant and have their own endpoint,
// so a missing tenant code can never be mistaken for a platform login.
func (s *Service) LoginPlatform(ctx context.Context, in LoginInput) (*Tokens, error) {
	return s.login(ctx, auth.LevelPlatform, "", in)
}

func (s *Service) login(ctx context.Context, level, tenantID string, in LoginInput) (*Tokens, error) {
	var (
		out        *Tokens
		rehashUser *user.User
		newHash    string
	)
	err := s.d.Tx(ctx, level, func(ctx context.Context) error {
		u, err := s.findByUsername(ctx, level, in.Username)
		if errors.Is(err, core.ErrNotFound) {
			s.d.Hasher.Dummy(in.Password)
			return ErrInvalidCredentials
		}
		if err != nil {
			return fmt.Errorf("session: find user: %w", err)
		}

		ok, needsRehash, verr := s.d.Hasher.Verify(in.Password, u.PasswordHash)
		if verr != nil {
			// A malformed stored hash is a data problem, not the caller's: log it, never reveal it.
			s.d.Log.Error("stored password hash is malformed", "user_id", u.ID)
			return ErrInvalidCredentials
		}
		if !ok || !u.IsActive {
			return ErrInvalidCredentials
		}

		if out, err = s.issue(ctx, u, tenantID, in.UserAgent, in.IP); err != nil {
			return err
		}
		if needsRehash {
			// Done after the transaction: a failed UPDATE would poison this transaction.
			if h, herr := s.d.Hasher.Hash(in.Password); herr == nil {
				rehashUser, newHash = u, h
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if rehashUser != nil {
		s.upgradeHash(ctx, level, rehashUser, newHash)
	}
	return out, nil
}

// upgradeHash stores a stronger hash. Failure is logged only: the login itself already succeeded.
func (s *Service) upgradeHash(ctx context.Context, level string, u *user.User, hash string) {
	err := s.d.Tx(ctx, level, func(ctx context.Context) error {
		return s.d.Users.SetPasswordHash(ctx, u, hash)
	})
	if err != nil {
		s.d.Log.Warn("could not upgrade password hash", "user_id", u.ID, "error", err)
	}
}

func (s *Service) findByUsername(ctx context.Context, level, username string) (*user.User, error) {
	if level == auth.LevelPlatform {
		return s.d.Users.FindPlatformByUsername(ctx, username)
	}
	return s.d.Users.FindByUsername(ctx, username)
}

func (s *Service) findByID(ctx context.Context, level, id string) (*user.User, error) {
	if level == auth.LevelPlatform {
		return s.d.Users.FindPlatformByID(ctx, id)
	}
	return s.d.Users.GetByID(ctx, id)
}

// issue creates the session row and both tokens. Must run inside a transaction from s.d.Tx.
func (s *Service) issue(ctx context.Context, u *user.User, tenantID, userAgent, ip string) (*Tokens, error) {
	now := s.d.Now()
	id := idgen.New()

	scope, level := scopePlatform, auth.LevelPlatform
	if tenantID != "" {
		scope, level = tenantID, auth.LevelTenant
	}
	refresh := scope + "." + idgen.NewOpaqueToken()

	row := &UserToken{
		ID:        id,
		UserID:    u.ID,
		TokenHash: hashToken(refresh),
		UserAgent: truncate(userAgent, 255),
		IPAddress: truncate(ip, 64),
		ExpiresAt: now.Add(s.d.RefreshTTL),
	}
	if tenantID != "" {
		row.TenantID = &tenantID
	}
	if err := s.d.Tokens.Create(ctx, row); err != nil {
		return nil, err
	}

	access, err := s.d.Issuer.Issue(auth.Claims{UserID: u.ID, TenantID: tenantID, TokenID: id, Level: level})
	if err != nil {
		return nil, fmt.Errorf("session: issue access token: %w", err)
	}
	return &Tokens{AccessToken: access, RefreshToken: refresh, ExpiresIn: s.d.AccessTTL}, nil
}

// Refresh exchanges a refresh token for a new pair and revokes the old one (rotation).
//
// A refresh token is single-use. If one that was already used shows up again, it was most likely copied,
// so every session of that user is revoked and the caller must log in again.
func (s *Service) Refresh(ctx context.Context, refreshToken, userAgent, ip string) (*Tokens, error) {
	level, tenantID, ok := parseRefreshToken(refreshToken)
	if !ok {
		return nil, ErrInvalidRefreshToken
	}
	if level == auth.LevelTenant {
		ctx = tenantctx.WithID(ctx, tenantID)
	}

	var (
		out    *Tokens
		reused bool
	)
	err := s.d.Tx(ctx, level, func(ctx context.Context) error {
		now := s.d.Now()
		row, err := s.d.Tokens.FindByHash(ctx, hashToken(refreshToken))
		if errors.Is(err, core.ErrNotFound) {
			return ErrInvalidRefreshToken
		}
		if err != nil {
			return fmt.Errorf("session: find refresh token: %w", err)
		}

		if row.RevokedAt != nil {
			// Reuse. Revoke everything and COMMIT (return nil): returning an error would roll the revocation back.
			if err := s.d.Tokens.RevokeAllForUser(ctx, row.UserID, now); err != nil {
				return err
			}
			reused = true
			return nil
		}
		if !now.Before(row.ExpiresAt) {
			return ErrInvalidRefreshToken
		}

		u, err := s.findByID(ctx, level, row.UserID)
		if errors.Is(err, core.ErrNotFound) {
			return ErrInvalidRefreshToken
		}
		if err != nil {
			return fmt.Errorf("session: find user: %w", err)
		}
		if !u.IsActive {
			return ErrInvalidRefreshToken
		}

		revoked, err := s.d.Tokens.Revoke(ctx, row.ID, now)
		if err != nil {
			return err
		}
		if !revoked { // another request used this token a moment ago
			return ErrInvalidRefreshToken
		}

		out, err = s.issue(ctx, u, tenantID, userAgent, ip)
		return err
	})
	if err != nil {
		return nil, err
	}
	if reused {
		s.d.Log.Warn("refresh token reuse detected, all sessions revoked", "tenant_id", tenantID)
		return nil, ErrInvalidRefreshToken
	}
	return out, nil
}

// Logout revokes the session of a refresh token. It is idempotent: an unknown or already revoked token is not an error.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	level, tenantID, ok := parseRefreshToken(refreshToken)
	if !ok {
		return nil
	}
	if level == auth.LevelTenant {
		ctx = tenantctx.WithID(ctx, tenantID)
	}
	return s.d.Tx(ctx, level, func(ctx context.Context) error {
		row, err := s.d.Tokens.FindByHash(ctx, hashToken(refreshToken))
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("session: find refresh token: %w", err)
		}
		_, err = s.d.Tokens.Revoke(ctx, row.ID, s.d.Now())
		return err
	})
}

// parseRefreshToken splits "<scope>.<opaque>" and returns the level and tenant_id the scope stands for.
func parseRefreshToken(raw string) (level, tenantID string, ok bool) {
	scope, opaque, found := strings.Cut(raw, ".")
	if !found || opaque == "" || len(raw) > 200 {
		return "", "", false
	}
	if scope == scopePlatform {
		return auth.LevelPlatform, "", true
	}
	id, err := uuid.Parse(scope)
	if err != nil || id.String() != scope { // only the canonical lowercase form
		return "", "", false
	}
	return auth.LevelTenant, scope, true
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
