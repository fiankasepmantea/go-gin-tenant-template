package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/tenant"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/user"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ratelimit"
	tenantctx "github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

var (
	// ErrInvalidResetToken is returned when a reset token is unknown, expired, already used or malformed.
	ErrInvalidResetToken = errors.New("session: invalid reset token")
	// ErrWrongPassword is returned by ChangePassword when the current password is not correct.
	ErrWrongPassword = errors.New("session: wrong current password")
	// ErrWeakPassword wraps the reason a new password was refused (too short, too long, same as the old one).
	ErrWeakPassword = errors.New("session: new password refused")
	// ErrAccountUnavailable is returned when the user of a valid token no longer exists or was deactivated.
	ErrAccountUnavailable = errors.New("session: account unavailable")
)

// AccountUsers is what Account needs from the user repository.
type AccountUsers interface {
	FindByLogin(ctx context.Context, identifier string) (*user.User, error)
	FindPlatformByLogin(ctx context.Context, identifier string) (*user.User, error)
	GetByID(ctx context.Context, id string) (*user.User, error)
	FindPlatformByID(ctx context.Context, id string) (*user.User, error)
	SetPasswordHash(ctx context.Context, u *user.User, hash string) error
}

// ResetStore keeps the reset requests (*ResetRepository).
type ResetStore interface {
	Create(ctx context.Context, p *PasswordReset) error
	FindByHash(ctx context.Context, hash []byte) (*PasswordReset, error)
	Consume(ctx context.Context, id string, at time.Time) (bool, error)
	InvalidateForUser(ctx context.Context, userID string, at time.Time) error
}

// AccountDeps are the collaborators of Account.
type AccountDeps struct {
	Tx       TxRunner
	Tenants  TenantFinder
	Users    AccountUsers
	Tokens   TokenStore
	Resets   ResetStore
	Hasher   *password.Hasher
	Notifier Notifier
	Limiter  Limiter // nil = no rate limiting
	Limits   Limits
	ResetTTL time.Duration // default 30 minutes
	Log      *slog.Logger
	Now      func() time.Time
}

// Account is the part of authentication that works on an existing user: changing the password and
// resetting a forgotten one.
type Account struct {
	d  AccountDeps
	wg sync.WaitGroup
}

// NewAccount validates the dependencies.
func NewAccount(d AccountDeps) (*Account, error) {
	if d.Tx == nil || d.Tenants == nil || d.Users == nil || d.Tokens == nil || d.Resets == nil || d.Hasher == nil || d.Notifier == nil {
		return nil, errors.New("session: missing dependency")
	}
	if d.ResetTTL <= 0 {
		d.ResetTTL = 30 * time.Minute
	}
	if d.Limits.Attempts <= 0 {
		d.Limits.Attempts = 5
	}
	if d.Limits.Window <= 0 {
		d.Limits.Window = 15 * time.Minute
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Account{d: d}, nil
}

// Wait blocks until the e-mails that are being sent have finished (graceful shutdown, tests).
func (a *Account) Wait() { a.wg.Wait() }

// ---- forgot password ----

// ResetRequest asks for a reset token for a user who forgot the password.
type ResetRequest struct {
	TenantCode string // ignored when Platform is set
	Platform   bool
	Identifier string // username or e-mail address
	IP         string
}

// RequestReset creates a reset token and e-mails it. It returns nil whether or not the account exists, so
// that the endpoint cannot be used to find out who has an account; only the rate limit (*RateLimitedError) and
// infrastructure failures are errors. The e-mail is sent in the background, so that the time of the answer does
// not tell either.
func (a *Account) RequestReset(ctx context.Context, in ResetRequest) error {
	kind := "tenant"
	if in.Platform {
		kind = "platform"
	}
	// Every request counts, found or not: each one may send an e-mail.
	checks := a.checks("reset", kind, in.TenantCode, in.Identifier, in.IP)
	if wait, blocked := a.blocked(ctx, checks); blocked {
		return &RateLimitedError{RetryAfter: wait}
	}
	if a.d.Limiter != nil {
		a.d.Limiter.Hit(ctx, checks)
	}

	level, tenantID := auth.LevelPlatform, ""
	if !in.Platform {
		code, ok := tenantctx.NormalizeCode(in.TenantCode)
		if !ok {
			return nil
		}
		t, err := a.d.Tenants.FindByCode(ctx, code)
		if errors.Is(err, core.ErrNotFound) || (err == nil && t.Status != tenant.StatusActive) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("session: find tenant: %w", err)
		}
		level, tenantID = auth.LevelTenant, t.ID
		ctx = tenantctx.WithID(ctx, t.ID)
	}

	var notice *ResetNotice
	err := a.d.Tx(ctx, level, func(ctx context.Context) error {
		u, err := a.findLogin(ctx, level, strings.TrimSpace(in.Identifier))
		if errors.Is(err, core.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("session: find user: %w", err)
		}
		if !u.IsActive || u.Email == "" {
			return nil
		}

		now := a.d.Now()
		// a new request replaces the older ones: only the latest e-mail works
		if err := a.d.Resets.InvalidateForUser(ctx, u.ID, now); err != nil {
			return err
		}
		scope := scopePlatform
		if tenantID != "" {
			scope = tenantID
		}
		token := scope + "." + idgen.NewOpaqueToken()
		row := &PasswordReset{
			ID: idgen.New(), UserID: u.ID, TokenHash: hashToken(token),
			IPAddress: truncate(in.IP, 64), ExpiresAt: now.Add(a.d.ResetTTL),
		}
		if tenantID != "" {
			row.TenantID = &tenantID
		}
		if err := a.d.Resets.Create(ctx, row); err != nil {
			return err
		}
		n := ResetNotice{Email: u.Email, Name: u.Name, Token: token, ExpiresAt: row.ExpiresAt}
		if tenantID != "" {
			n.TenantCode = strings.ToLower(strings.TrimSpace(in.TenantCode))
		}
		notice = &n
		return nil
	})
	if err != nil {
		return err
	}
	if notice != nil {
		a.send(ctx, *notice)
	}
	return nil
}

func (a *Account) send(ctx context.Context, n ResetNotice) {
	// detached from the request: the answer must not wait for the mail server, and a client that hangs up
	// must not cancel the mail
	bg := context.WithoutCancel(ctx)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ctx, cancel := context.WithTimeout(bg, 30*time.Second)
		defer cancel()
		if err := a.d.Notifier.PasswordReset(ctx, n); err != nil {
			a.d.Log.Error("could not send the password reset message", "error", err)
		}
	}()
}

func (a *Account) findLogin(ctx context.Context, level, identifier string) (*user.User, error) {
	if level == auth.LevelPlatform {
		return a.d.Users.FindPlatformByLogin(ctx, identifier)
	}
	return a.d.Users.FindByLogin(ctx, identifier)
}

func (a *Account) findByID(ctx context.Context, level, id string) (*user.User, error) {
	if level == auth.LevelPlatform {
		return a.d.Users.FindPlatformByID(ctx, id)
	}
	return a.d.Users.GetByID(ctx, id)
}

// ---- reset with the token ----

// ResetPassword sets a new password with the token from the e-mail. The token is single use. All sessions of
// the user end, so that whoever had access with the old password is signed out.
func (a *Account) ResetPassword(ctx context.Context, token, newPassword, ip string) error {
	level, tenantID, ok := parseRefreshToken(token) // "<scope>.<opaque>", same format
	if !ok {
		return ErrInvalidResetToken
	}
	if err := checkNewPassword(newPassword); err != nil {
		return err
	}
	// guessing tokens is hopeless (256 bits), but a flood of attempts still costs a database round trip each
	checks := []ratelimit.Check{{Key: "reset-token:ip:" + ip, Limit: 6 * a.d.Limits.Attempts, Window: a.d.Limits.Window}}
	if wait, blocked := a.blocked(ctx, checks); blocked {
		return &RateLimitedError{RetryAfter: wait}
	}
	if level == auth.LevelTenant {
		ctx = tenantctx.WithID(ctx, tenantID)
	}

	err := a.d.Tx(ctx, level, func(ctx context.Context) error {
		now := a.d.Now()
		row, err := a.d.Resets.FindByHash(ctx, hashToken(token))
		if errors.Is(err, core.ErrNotFound) {
			return ErrInvalidResetToken
		}
		if err != nil {
			return err
		}
		if row.UsedAt != nil || !now.Before(row.ExpiresAt) {
			return ErrInvalidResetToken
		}
		u, err := a.findByID(ctx, level, row.UserID)
		if errors.Is(err, core.ErrNotFound) || (err == nil && !u.IsActive) {
			return ErrInvalidResetToken
		}
		if err != nil {
			return fmt.Errorf("session: find user: %w", err)
		}
		hash, err := a.d.Hasher.Hash(newPassword)
		if err != nil {
			return fmt.Errorf("session: hash password: %w", err)
		}
		// the guard against two requests with one token: only one of them gets true
		if used, err := a.d.Resets.Consume(ctx, row.ID, now); err != nil {
			return err
		} else if !used {
			return ErrInvalidResetToken
		}
		return a.apply(ctx, u, hash, now)
	})
	if errors.Is(err, ErrInvalidResetToken) && a.d.Limiter != nil {
		a.d.Limiter.Hit(ctx, checks)
	}
	return err
}

// apply stores the new hash, ends every session and cancels the other open reset requests.
func (a *Account) apply(ctx context.Context, u *user.User, hash string, now time.Time) error {
	if err := a.d.Users.SetPasswordHash(ctx, u, hash); err != nil {
		return fmt.Errorf("session: set password: %w", err)
	}
	if err := a.d.Tokens.RevokeAllForUser(ctx, u.ID, now); err != nil {
		return err
	}
	return a.d.Resets.InvalidateForUser(ctx, u.ID, now)
}

// ---- change (logged in) ----

// ChangeInput is a password change by a signed-in user. The identity comes from the verified access token.
type ChangeInput struct {
	UserID   string
	TenantID string // empty for a platform admin
	Level    string
	Current  string
	New      string
	IP       string
}

// ChangePassword checks the current password and sets a new one. Every session of the user ends, including
// the current one: the client must log in again. Wrong current passwords are rate limited per user, because a
// stolen access token must not be a way to guess the password.
func (a *Account) ChangePassword(ctx context.Context, in ChangeInput) error {
	if err := checkNewPassword(in.New); err != nil {
		return err
	}
	checks := []ratelimit.Check{{Key: "change:user:" + in.UserID, Limit: a.d.Limits.Attempts, Window: a.d.Limits.Window}}
	if wait, blocked := a.blocked(ctx, checks); blocked {
		return &RateLimitedError{RetryAfter: wait}
	}
	if in.Level == auth.LevelTenant {
		ctx = tenantctx.WithID(ctx, in.TenantID)
	}

	err := a.d.Tx(ctx, in.Level, func(ctx context.Context) error {
		u, err := a.findByID(ctx, in.Level, in.UserID)
		if errors.Is(err, core.ErrNotFound) {
			return ErrAccountUnavailable
		}
		if err != nil {
			return fmt.Errorf("session: find user: %w", err)
		}
		if !u.IsActive {
			return ErrAccountUnavailable
		}
		ok, _, verr := a.d.Hasher.Verify(in.Current, u.PasswordHash)
		if verr != nil {
			a.d.Log.Error("stored password hash is malformed", "user_id", u.ID)
			return ErrWrongPassword
		}
		if !ok {
			return ErrWrongPassword
		}
		if same, _, _ := a.d.Hasher.Verify(in.New, u.PasswordHash); same {
			return fmt.Errorf("%w: the new password must differ from the current one", ErrWeakPassword)
		}
		hash, err := a.d.Hasher.Hash(in.New)
		if err != nil {
			return fmt.Errorf("session: hash password: %w", err)
		}
		return a.apply(ctx, u, hash, a.d.Now())
	})
	switch {
	case errors.Is(err, ErrWrongPassword) && a.d.Limiter != nil:
		a.d.Limiter.Hit(ctx, checks)
	case err == nil && a.d.Limiter != nil:
		a.d.Limiter.Reset(ctx, checks)
	}
	return err
}

// checkNewPassword applies the password policy to a new password.
func checkNewPassword(plain string) error {
	if err := password.Validate(plain); err != nil {
		return fmt.Errorf("%w: %w", ErrWeakPassword, err)
	}
	return nil
}

// ---- rate limit helpers ----

func (a *Account) blocked(ctx context.Context, checks []ratelimit.Check) (time.Duration, bool) {
	if a.d.Limiter == nil {
		return 0, false
	}
	return a.d.Limiter.Blocked(ctx, checks)
}

func (a *Account) checks(action, kind, tenantCode, identifier, ip string) []ratelimit.Check {
	n, w := a.d.Limits.Attempts, a.d.Limits.Window
	if kind == "platform" {
		tenantCode = ""
	}
	acct := kind + "|" + strings.ToLower(strings.TrimSpace(tenantCode)) + "|" + strings.ToLower(strings.TrimSpace(identifier))
	return []ratelimit.Check{
		{Key: action + ":acct-ip:" + acct + "|" + ip, Limit: n, Window: w},
		{Key: action + ":ip:" + kind + "|" + ip, Limit: 6 * n, Window: w},
		{Key: action + ":acct:" + acct, Limit: 10 * n, Window: w},
	}
}
