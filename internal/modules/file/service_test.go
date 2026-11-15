package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

var pngHead = []byte("\x89PNG\r\n\x1a\n")

func png(n int) []byte { return append(append([]byte{}, pngHead...), bytes.Repeat([]byte{1}, n)...) }

type fakeRepo struct {
	rows    map[string]*File
	failOn  bool
	deleted []string
}

func newFakeRepo() *fakeRepo { return &fakeRepo{rows: map[string]*File{}} }

func (r *fakeRepo) Create(_ context.Context, f *File) error {
	if r.failOn {
		return errors.New("database is down")
	}
	r.rows[f.ID] = f
	return nil
}
func (r *fakeRepo) GetByID(_ context.Context, id string) (*File, error) {
	if f, ok := r.rows[id]; ok {
		return f, nil
	}
	return nil, core.ErrNotFound
}
func (r *fakeRepo) List(context.Context, core.ListParams) (core.Page[File], error) {
	return core.Page[File]{}, nil
}
func (r *fakeRepo) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	delete(r.rows, id)
	return nil
}

func newTestService(t *testing.T, max int64) (*Service, *fakeRepo, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "blobs")
	blobs, err := storage.NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	repo := newFakeRepo()
	return NewService(repo, blobs, Options{MaxBytes: max}), repo, dir
}

func blobCount(dir string) int {
	m, _ := filepath.Glob(filepath.Join(dir, "*", "*"))
	return len(m)
}

func tctx() context.Context { return tenant.WithID(context.Background(), "t1") }

func TestService_UploadStoresRowAndContent(t *testing.T) {
	s, repo, dir := newTestService(t, 1<<20)
	content := png(3000)

	f, err := s.Upload(tctx(), UploadInput{Name: `..\x/photo.png`, Purpose: " avatar ", UploadedBy: "u1", Content: bytes.NewReader(content)})
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "photo.png" || f.ContentType != "image/png" || f.Purpose != "avatar" || f.Size != int64(len(content)) ||
		f.UploadedBy == nil || *f.UploadedBy != "u1" || f.StorageKey != "t1/"+f.ID {
		t.Fatalf("file: %+v", f)
	}
	if repo.rows[f.ID] == nil || blobCount(dir) != 1 {
		t.Fatal("row and content must both exist")
	}

	f2, rc, err := s.Open(tctx(), f.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if f2.ID != f.ID || !bytes.Equal(got, content) {
		t.Fatalf("content differs: %d bytes", len(got))
	}

	if err := s.Delete(tctx(), f.ID); err != nil {
		t.Fatal(err)
	}
	if blobCount(dir) != 0 || len(repo.deleted) != 1 {
		t.Fatalf("delete left content=%d deleted=%v", blobCount(dir), repo.deleted)
	}
	if err := s.Delete(tctx(), f.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestService_UploadRefusals(t *testing.T) {
	s, repo, dir := newTestService(t, 100)
	cases := []struct {
		name    string
		in      UploadInput
		ctx     context.Context
		wantErr error
	}{
		{"html named png", UploadInput{Name: "a.png", Content: strings.NewReader("<html><script>x</script></html>")}, tctx(), ErrTypeNotAllowed},
		{"svg", UploadInput{Name: "a.svg", Content: strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)}, tctx(), ErrTypeNotAllowed},
		{"empty", UploadInput{Name: "a.png", Content: strings.NewReader("")}, tctx(), ErrEmpty},
		{"too large", UploadInput{Name: "a.png", Content: bytes.NewReader(png(200))}, tctx(), ErrTooLarge},
		{"bad purpose", UploadInput{Name: "a.png", Purpose: "Avatar!", Content: bytes.NewReader(png(5))}, tctx(), ErrInvalidPurpose},
		{"no tenant", UploadInput{Name: "a.png", Content: bytes.NewReader(png(5))}, context.Background(), core.ErrNoTenant},
	}
	for _, c := range cases {
		if _, err := s.Upload(c.ctx, c.in); !errors.Is(err, c.wantErr) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.wantErr)
		}
	}
	if len(repo.rows) != 0 || blobCount(dir) != 0 {
		t.Fatalf("refused uploads left rows=%d content=%d", len(repo.rows), blobCount(dir))
	}
}

func TestService_FailedRowRemovesTheContent(t *testing.T) {
	s, repo, dir := newTestService(t, 1<<20)
	repo.failOn = true
	if _, err := s.Upload(tctx(), UploadInput{Name: "a.png", Content: bytes.NewReader(png(10))}); err == nil {
		t.Fatal("expected the database error")
	}
	if blobCount(dir) != 0 {
		t.Fatal("content must be removed when the row cannot be stored")
	}
}

func TestService_OpenWithMissingContentIsNotFound(t *testing.T) {
	s, repo, _ := newTestService(t, 1<<20)
	repo.rows["x"] = &File{ID: "x", StorageKey: "t1/x"}
	if _, _, err := s.Open(tctx(), "x"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, _, err := s.Open(tctx(), "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestService_Defaults(t *testing.T) {
	s := NewService(newFakeRepo(), nil, Options{})
	if s.MaxBytes() != DefaultMaxBytes {
		t.Fatalf("max = %d", s.MaxBytes())
	}
	types := s.Types()
	if len(types) != len(DefaultTypes) || types[0] != "image/png" {
		t.Fatalf("types = %v", types)
	}
	for _, bad := range types {
		if bad == "image/svg+xml" {
			t.Fatal("svg must not be allowed by default")
		}
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"photo.png":              "photo.png",
		`C:\Users\me\photo.png`:  "photo.png",
		"../../etc/passwd":       "passwd",
		"a/b/":                   "file",
		"":                       "file",
		"..":                     "file",
		"  spaced name.pdf  ":    "spaced name.pdf",
		"bad\x00\r\nname.png":    "badname.png",
		"laporan akhir é 日本.pdf": "laporan akhir é 日本.pdf",
	}
	for in, want := range cases {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := cleanName(strings.Repeat("a", 400)); len([]rune(got)) != maxNameRunes {
		t.Errorf("long name has %d characters", len([]rune(got)))
	}
}
