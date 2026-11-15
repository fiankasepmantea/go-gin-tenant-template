package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
)

type fakeStore struct {
	created *User
	changed map[string]any
	deleted string
	err     error
}

func (f *fakeStore) Create(_ context.Context, e *User) error { f.created = e; return f.err }
func (f *fakeStore) GetByID(_ context.Context, id string) (*User, error) {
	return &User{ID: id, IsActive: true}, f.err
}
func (f *fakeStore) List(context.Context, core.ListParams) (core.Page[User], error) {
	return core.Page[User]{}, nil
}
func (f *fakeStore) Update(_ context.Context, _ string, fields map[string]any) error {
	f.changed = fields
	return f.err
}
func (f *fakeStore) Delete(_ context.Context, id string) error { f.deleted = id; return f.err }

type fakeSessions struct{ revoked []string }

func (f *fakeSessions) RevokeAllForUser(_ context.Context, id string, _ time.Time) error {
	f.revoked = append(f.revoked, id)
	return nil
}

type fakeHasher struct{}

func (fakeHasher) Hash(plain string) (string, error) { return "hash:" + plain, nil }

func newSvc() (*Service, *fakeStore, *fakeSessions) {
	st, se := &fakeStore{}, &fakeSessions{}
	return NewService(st, se, fakeHasher{}), st, se
}

func TestService_Create(t *testing.T) {
	s, st, _ := newSvc()
	ctx := context.Background()

	u, err := s.Create(ctx, CreateInput{Username: " budi ", Email: " b@x.test ", Name: " Budi ", Password: "a-long-enough-password"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "budi" || u.Email != "b@x.test" || u.Name != "Budi" || !u.IsActive || u.PasswordHash != "hash:a-long-enough-password" {
		t.Fatalf("user: %+v", u)
	}
	if st.created != u {
		t.Fatal("the user was not stored")
	}

	off := false
	if u, _ := s.Create(ctx, CreateInput{Username: "x", Password: "a-long-enough-password", IsActive: &off}); u.IsActive {
		t.Fatal("is_active=false must be kept")
	}

	for _, pw := range []string{"short", strings.Repeat("a", password.MaxLength+1)} {
		st.created = nil
		if _, err := s.Create(ctx, CreateInput{Username: "x", Password: pw}); !errors.Is(err, ErrWeakPassword) || st.created != nil {
			t.Fatalf("password of %d characters: %v", len(pw), err)
		}
	}
}

func TestService_UpdateEndsSessionsOnlyWhenDeactivating(t *testing.T) {
	s, st, se := newSvc()
	ctx := context.Background()
	on, off, empty := true, false, ""
	name := " Budi S "

	if _, err := s.Update(ctx, "admin", "u1", Changes{Name: &name, IsActive: &on}); err != nil {
		t.Fatal(err)
	}
	if st.changed["name"] != "Budi S" || len(se.revoked) != 0 {
		t.Fatalf("changed=%v revoked=%v", st.changed, se.revoked)
	}
	if _, err := s.Update(ctx, "admin", "u1", Changes{GroupID: &empty}); err != nil {
		t.Fatal(err)
	}
	if v, ok := st.changed["group_id"]; !ok || v != nil {
		t.Fatalf(`group_id "" must become NULL: %v`, st.changed)
	}
	if _, err := s.Update(ctx, "admin", "u1", Changes{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	if len(se.revoked) != 1 || se.revoked[0] != "u1" {
		t.Fatalf("revoked = %v", se.revoked)
	}
}

func TestService_NotOnYourOwnAccount(t *testing.T) {
	s, st, se := newSvc()
	ctx := context.Background()
	off := false
	if _, err := s.Update(ctx, "me", "me", Changes{IsActive: &off}); !errors.Is(err, ErrOwnAccount) || st.changed != nil {
		t.Fatalf("deactivate self: %v", err)
	}
	if err := s.Delete(ctx, "me", "me"); !errors.Is(err, ErrOwnAccount) || st.deleted != "" {
		t.Fatalf("delete self: %v", err)
	}
	// changing your own name is fine
	name := "New"
	if _, err := s.Update(ctx, "me", "me", Changes{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if len(se.revoked) != 0 {
		t.Fatalf("revoked = %v", se.revoked)
	}
}

func TestService_DeleteAndSetPasswordEndSessions(t *testing.T) {
	s, st, se := newSvc()
	ctx := context.Background()
	if err := s.Delete(ctx, "admin", "u1"); err != nil || st.deleted != "u1" {
		t.Fatalf("delete: %v", err)
	}
	if err := s.SetPassword(ctx, "u2", "another-long-password"); err != nil {
		t.Fatal(err)
	}
	if st.changed["password_hash"] != "hash:another-long-password" {
		t.Fatalf("changed = %v", st.changed)
	}
	if len(se.revoked) != 2 {
		t.Fatalf("revoked = %v", se.revoked)
	}
	if err := s.SetPassword(ctx, "u2", "short"); !errors.Is(err, ErrWeakPassword) || len(se.revoked) != 2 {
		t.Fatalf("weak password: %v", err)
	}
}

func TestService_StoreErrorsPass(t *testing.T) {
	s, st, se := newSvc()
	st.err = core.ErrNotFound
	if err := s.Delete(context.Background(), "a", "b"); !errors.Is(err, core.ErrNotFound) || len(se.revoked) != 0 {
		t.Fatalf("a failed delete must not revoke: %v %v", err, se.revoked)
	}
}
