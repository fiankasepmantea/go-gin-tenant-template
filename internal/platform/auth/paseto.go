// Package auth issues and verifies PASETO v4 access tokens (local, encrypted).
package auth

import (
	"errors"
	"fmt"
	"time"

	"aidanwoods.dev/go-paseto"
)

// Access level of the token owner.
const (
	// LevelPlatform is a platform admin ("building owner"): not bound to a single tenant.
	LevelPlatform = "platform"
	// LevelTenant is a regular user or admin within a single tenant ("tenant").
	LevelTenant = "tenant"
)

const (
	claimTenant = "tid"
	claimLevel  = "lvl"
)

// ErrInvalidToken is returned for tokens that are corrupted, expired, or not ours.
// The cause is deliberately not distinguished so as not to leak information to an attacker.
var ErrInvalidToken = errors.New("auth: invalid or expired token")

// Claims is the token content used by the application.
type Claims struct {
	UserID   string // the user who logged in
	TenantID string // empty for LevelPlatform
	TokenID  string // binds the access token to a single session (user_tokens row)
	Level    string // LevelPlatform or LevelTenant
}

func (c Claims) validate() error {
	if c.UserID == "" {
		return errors.New("auth: UserID is required")
	}
	if c.TokenID == "" {
		return errors.New("auth: TokenID is required")
	}
	switch c.Level {
	case LevelPlatform:
	case LevelTenant:
		if c.TenantID == "" {
			return errors.New("auth: TenantID is required for tenant level")
		}
	default:
		return fmt.Errorf("auth: unknown level %q", c.Level)
	}
	return nil
}

// Manager issues and verifies tokens.
type Manager struct {
	key       paseto.V4SymmetricKey
	issuer    string
	accessTTL time.Duration
	now       func() time.Time
}

// GenerateKeyHex creates a new symmetric key (hex) for PASETO_KEY.
func GenerateKeyHex() string {
	return paseto.NewV4SymmetricKey().ExportHex()
}

// NewManager creates a Manager from a hex key (32 bytes).
func NewManager(keyHex, issuer string, accessTTL time.Duration) (*Manager, error) {
	key, err := paseto.V4SymmetricKeyFromHex(keyHex)
	if err != nil {
		return nil, fmt.Errorf("auth: invalid PASETO key: %w", err)
	}
	if accessTTL <= 0 {
		return nil, errors.New("auth: accessTTL must be greater than 0")
	}
	return &Manager{key: key, issuer: issuer, accessTTL: accessTTL, now: time.Now}, nil
}

// Issue creates an access token for the given claims.
func (m *Manager) Issue(c Claims) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}

	now := m.now()
	t := paseto.NewToken()
	t.SetIssuer(m.issuer)
	t.SetSubject(c.UserID)
	t.SetJti(c.TokenID)
	t.SetIssuedAt(now)
	t.SetNotBefore(now)
	t.SetExpiration(now.Add(m.accessTTL))
	t.SetString(claimTenant, c.TenantID)
	t.SetString(claimLevel, c.Level)

	return t.V4Encrypt(m.key, nil), nil
}

// Parse verifies the token and returns its claims.
func (m *Manager) Parse(token string) (*Claims, error) {
	parser := paseto.NewParserWithoutExpiryCheck()
	parser.AddRule(paseto.IssuedBy(m.issuer), paseto.ValidAt(m.now()))

	t, err := parser.ParseV4Local(m.key, token, nil)
	if err != nil {
		return nil, ErrInvalidToken
	}

	userID, err := t.GetSubject()
	if err != nil {
		return nil, ErrInvalidToken
	}
	tokenID, err := t.GetJti()
	if err != nil {
		return nil, ErrInvalidToken
	}
	tenantID, err := t.GetString(claimTenant)
	if err != nil {
		return nil, ErrInvalidToken
	}
	level, err := t.GetString(claimLevel)
	if err != nil {
		return nil, ErrInvalidToken
	}

	c := &Claims{UserID: userID, TenantID: tenantID, TokenID: tokenID, Level: level}
	if err := c.validate(); err != nil {
		return nil, ErrInvalidToken
	}
	return c, nil
}
