package group

import (
	"context"
	"errors"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

type fakeStore struct {
	created *Group
	changed map[string]any
	users   int64
	deleted bool
}

func (f *fakeStore) Create(_ context.Context, e *Group) error { f.created = e; return nil }
func (f *fakeStore) GetByID(context.Context, string) (*Group, error) {
	return nil, core.ErrNotFound
}
func (f *fakeStore) List(context.Context, core.ListParams) (core.Page[Group], error) {
	return core.Page[Group]{}, nil
}
func (f *fakeStore) Update(_ context.Context, _ string, fields map[string]any) error {
	f.changed = fields
	return nil
}
func (f *fakeStore) Delete(context.Context, string) error              { f.deleted = true; return nil }
func (f *fakeStore) CountUsers(context.Context, string) (int64, error) { return f.users, nil }

func TestService_TrimsTheName(t *testing.T) {
	f := &fakeStore{}
	s := NewService(f)
	ctx := context.Background()

	if err := s.Create(ctx, &Group{Name: "  admin \t"}); err != nil || f.created.Name != "admin" {
		t.Fatalf("create: %v %+v", err, f.created)
	}
	if err := s.Update(ctx, "id", map[string]any{"name": " finance ", "description": " keep "}); err != nil {
		t.Fatal(err)
	}
	if f.changed["name"] != "finance" || f.changed["description"] != " keep " {
		t.Fatalf("update: %v", f.changed)
	}
	// no name sent: nothing is invented
	if err := s.Update(ctx, "id", map[string]any{"description": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.changed["name"]; ok {
		t.Fatal("a name appeared out of nowhere")
	}
}

func TestService_DeleteRefusesAGroupWithUsers(t *testing.T) {
	ctx := context.Background()

	f := &fakeStore{users: 2}
	if err := NewService(f).Delete(ctx, "id"); !errors.Is(err, core.ErrInUse) || f.deleted {
		t.Fatalf("a group with users: err = %v, deleted = %v", err, f.deleted)
	}

	f = &fakeStore{}
	if err := NewService(f).Delete(ctx, "id"); err != nil || !f.deleted {
		t.Fatalf("an empty group: err = %v, deleted = %v", err, f.deleted)
	}
}
