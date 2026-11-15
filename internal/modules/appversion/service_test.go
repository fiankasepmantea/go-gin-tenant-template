package appversion

import (
	"context"
	"errors"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
)

type fakeStore struct{ rows []AppVersion }

func (f *fakeStore) Create(_ context.Context, e *AppVersion) error {
	for _, r := range f.rows {
		if r.Platform == e.Platform && r.Version == e.Version {
			return core.ErrConflict
		}
	}
	e.ID = "id-" + e.Version
	f.rows = append(f.rows, *e)
	return nil
}

func (f *fakeStore) GetByID(_ context.Context, id string) (*AppVersion, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			return &f.rows[i], nil
		}
	}
	return nil, core.ErrNotFound
}

func (f *fakeStore) List(context.Context, core.ListParams) (core.Page[AppVersion], error) {
	return core.Page[AppVersion]{}, nil
}
func (f *fakeStore) Update(context.Context, string, map[string]any) error { return nil }
func (f *fakeStore) Delete(context.Context, string) error                 { return nil }
func (f *fakeStore) ByPlatform(_ context.Context, p string) ([]AppVersion, error) {
	var out []AppVersion
	for _, r := range f.rows {
		if r.Platform == p {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestCheck(t *testing.T) {
	st := &fakeStore{rows: []AppVersion{
		{ID: "a", Platform: "android", Version: "1.9.0", MinSupportedVersion: "0.0.0"},
		{ID: "b", Platform: "android", Version: "1.10.0", MinSupportedVersion: "1.5.0"},
	}}
	s := NewService(st, nil, Options{})
	cases := []struct {
		cur           string
		update, force bool
	}{
		{"1.9.0", true, false},
		{"1.4", true, true},
		{"1.10", false, false},
		{"3.0", false, false},
	}
	for _, c := range cases {
		got, err := s.Check(context.Background(), "android", c.cur)
		if err != nil || got.UpdateAvailable != c.update || got.ForceUpdate != c.force || got.LatestVersion != "1.10.0" {
			t.Errorf("Check(%q) = %+v, %v", c.cur, got, err)
		}
	}
	var ve *ValidationError
	if _, err := s.Check(context.Background(), "android", "x"); !errors.As(err, &ve) {
		t.Errorf("bad version: %v", err)
	}
	if _, err := s.Check(context.Background(), "ios", "1.0"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("no versions: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	s := NewService(&fakeStore{}, nil, Options{})
	ctx := context.Background()
	bad := []CreateInput{
		{Platform: "palm", Version: "1.0"},
		{Platform: "android", Version: "v1"},
		{Platform: "android", Version: "1.0", MinSupportedVersion: "2.0"},
		{Platform: "android", Version: "1.0", StoreURL: "javascript:x"},
	}
	for _, in := range bad {
		var ve *ValidationError
		if _, err := s.Create(ctx, in); !errors.As(err, &ve) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	v, err := s.Create(ctx, CreateInput{Platform: "android", Version: "1.2"})
	if err != nil || v.Version != "1.2.0" || v.MinSupportedVersion != "0.0.0" {
		t.Fatalf("create: %+v %v", v, err)
	}
	if _, err := s.Create(ctx, CreateInput{Platform: "android", Version: "1.2.0"}); !errors.Is(err, core.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
}
