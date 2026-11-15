package user

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
)

var (
	// ErrWeakPassword wraps the reason a password was refused (password.ErrTooShort or password.ErrTooLong).
	ErrWeakPassword = errors.New("user: password refused")
	// ErrOwnAccount: an administrator cannot deactivate or delete their own account, so a tenant cannot lose
	// its last administrator by accident.
	ErrOwnAccount = errors.New("user: not allowed on your own account")
)

// Store is what the service needs from the data layer. *Repository satisfies it, and a test can pass a fake.
type Store interface {
	Create(ctx context.Context, e *User) error
	GetByID(ctx context.Context, id string) (*User, error)
	List(ctx context.Context, p core.ListParams) (core.Page[User], error)
	Update(ctx context.Context, id string, fields map[string]any) error
	Delete(ctx context.Context, id string) error
}

// Sessions ends the sessions of a user. *session.Repository satisfies it.
type Sessions interface {
	RevokeAllForUser(ctx context.Context, userID string, at time.Time) error
}

// Hasher hashes new passwords. *password.Hasher satisfies it.
type Hasher interface {
	Hash(plain string) (string, error)
}

// CreateInput is what is needed to create a user of the tenant in the context.
type CreateInput struct {
	Username string
	Email    string
	Name     string
	Password string
	GroupID  *string // nil: no group
	IsActive *bool   // nil: active
}

// Changes are the fields of a user that can change; nil means "leave as is". GroupID "" removes the group.
type Changes struct {
	Email    *string
	Name     *string
	GroupID  *string
	IsActive *bool
}

// Service holds the business rules of the users of a tenant. The handler calls the service, the service calls
// the repository. The tenant comes from the context, never from the input.
type Service struct {
	store    Store
	sessions Sessions
	hasher   Hasher
	now      func() time.Time
}

// NewService creates the service.
func NewService(store Store, sessions Sessions, hasher Hasher) *Service {
	return &Service{store: store, sessions: sessions, hasher: hasher, now: time.Now}
}

func (s *Service) List(ctx context.Context, p core.ListParams) (core.Page[User], error) {
	return s.store.List(ctx, p)
}

func (s *Service) GetByID(ctx context.Context, id string) (*User, error) {
	return s.store.GetByID(ctx, id)
}

// Create checks the password policy, hashes the password and stores the user.
func (s *Service) Create(ctx context.Context, in CreateInput) (*User, error) {
	hash, err := s.hashNew(in.Password)
	if err != nil {
		return nil, err
	}
	u := &User{
		Username:     strings.TrimSpace(in.Username),
		Email:        strings.TrimSpace(in.Email),
		Name:         strings.TrimSpace(in.Name),
		PasswordHash: hash,
		GroupID:      in.GroupID,
		IsActive:     in.IsActive == nil || *in.IsActive,
	}
	if err := s.store.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Update changes the given fields. Deactivating a user ends their sessions; the caller (actorID) cannot
// deactivate themselves.
func (s *Service) Update(ctx context.Context, actorID, id string, ch Changes) (*User, error) {
	deactivate := ch.IsActive != nil && !*ch.IsActive
	if deactivate && actorID == id {
		return nil, ErrOwnAccount
	}
	fields := map[string]any{}
	if ch.Email != nil {
		fields["email"] = strings.TrimSpace(*ch.Email)
	}
	if ch.Name != nil {
		fields["name"] = strings.TrimSpace(*ch.Name)
	}
	if ch.GroupID != nil {
		if *ch.GroupID == "" {
			fields["group_id"] = nil
		} else {
			fields["group_id"] = *ch.GroupID
		}
	}
	if ch.IsActive != nil {
		fields["is_active"] = *ch.IsActive
	}
	if err := s.store.Update(ctx, id, fields); err != nil {
		return nil, err
	}
	if deactivate {
		if err := s.sessions.RevokeAllForUser(ctx, id, s.now()); err != nil {
			return nil, err
		}
	}
	return s.store.GetByID(ctx, id)
}

// Delete deletes a user and ends their sessions. The caller (actorID) cannot delete themselves.
func (s *Service) Delete(ctx context.Context, actorID, id string) error {
	if actorID == id {
		return ErrOwnAccount
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	return s.sessions.RevokeAllForUser(ctx, id, s.now())
}

// SetPassword lets an administrator set a new password. The sessions of the user end, so the old password (and
// anyone holding a token for it) stops working at once.
func (s *Service) SetPassword(ctx context.Context, id, plain string) error {
	hash, err := s.hashNew(plain)
	if err != nil {
		return err
	}
	if err := s.store.Update(ctx, id, map[string]any{"password_hash": hash}); err != nil {
		return err
	}
	return s.sessions.RevokeAllForUser(ctx, id, s.now())
}

func (s *Service) hashNew(plain string) (string, error) {
	if err := password.Validate(plain); err != nil {
		return "", fmt.Errorf("%w: %w", ErrWeakPassword, err)
	}
	hash, err := s.hasher.Hash(plain)
	if err != nil {
		return "", fmt.Errorf("user: hash password: %w", err)
	}
	return hash, nil
}
