package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/tenant"
)

// DefaultMaxBytes is the upload limit when none is configured.
const DefaultMaxBytes = 10 << 20

// DefaultTypes are the content types that can be uploaded. The type is detected from the bytes of the file,
// what the client says is ignored. SVG is left out on purpose: it can carry scripts.
var DefaultTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}

const maxNameRunes = 255

var purposeRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,49}$`)

var (
	// ErrEmpty: the upload has no content.
	ErrEmpty = errors.New("file: empty")
	// ErrTooLarge: the upload is larger than the limit.
	ErrTooLarge = errors.New("file: too large")
	// ErrTypeNotAllowed: the detected content type is not on the list.
	ErrTypeNotAllowed = errors.New("file: type not allowed")
	// ErrInvalidPurpose: the purpose is not a short lowercase word.
	ErrInvalidPurpose = errors.New("file: invalid purpose")
)

// Store is what the service needs from the data layer. *Repository satisfies it, and a test can pass a fake.
type Store interface {
	Create(ctx context.Context, e *File) error
	GetByID(ctx context.Context, id string) (*File, error)
	List(ctx context.Context, p core.ListParams) (core.Page[File], error)
	Delete(ctx context.Context, id string) error
}

// Options configure the service.
type Options struct {
	MaxBytes int64    // largest file; DefaultMaxBytes when zero
	Types    []string // allowed content types; DefaultTypes when empty
	Log      *slog.Logger
}

// Service holds the business rules of files: what may be uploaded, where the content goes, and that the row and
// the content are created and removed together. The handler calls the service, the service calls the repository
// and the storage.
type Service struct {
	repo    Store
	blobs   storage.Store
	max     int64
	allowed map[string]bool
	log     *slog.Logger
}

// NewService creates the service.
func NewService(repo Store, blobs storage.Store, opt Options) *Service {
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = DefaultMaxBytes
	}
	if len(opt.Types) == 0 {
		opt.Types = DefaultTypes
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	allowed := make(map[string]bool, len(opt.Types))
	for _, t := range opt.Types {
		allowed[t] = true
	}
	return &Service{repo: repo, blobs: blobs, max: opt.MaxBytes, allowed: allowed, log: opt.Log}
}

// MaxBytes is the largest file that is accepted.
func (s *Service) MaxBytes() int64 { return s.max }

// Types lists the content types that are accepted, the default ones first.
func (s *Service) Types() []string {
	out := make([]string, 0, len(s.allowed))
	for _, t := range DefaultTypes {
		if s.allowed[t] {
			out = append(out, t)
		}
	}
	for t := range s.allowed {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// UploadInput is one file to store for the tenant in the context.
type UploadInput struct {
	Name       string // as the client named it; cleaned before it is stored
	Purpose    string // optional: what the file is for, e.g. avatar
	UploadedBy string // user id, may be empty
	Content    io.Reader
}

// Upload checks the file, stores the content and then the row. When the row cannot be stored the content is
// removed again, so a failed upload leaves nothing behind.
func (s *Service) Upload(ctx context.Context, in UploadInput) (*File, error) {
	tenantID, ok := tenant.IDFrom(ctx)
	if !ok {
		return nil, core.ErrNoTenant
	}
	purpose := strings.TrimSpace(in.Purpose)
	if purpose != "" && !purposeRe.MatchString(purpose) {
		return nil, ErrInvalidPurpose
	}

	head := make([]byte, 512)
	n, err := io.ReadFull(in.Content, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n == 0 {
		return nil, ErrEmpty
	}
	head = head[:n]
	ctype, _, _ := strings.Cut(http.DetectContentType(head), ";")
	if !s.allowed[ctype] {
		return nil, ErrTypeNotAllowed
	}

	f := &File{ID: idgen.New(), Name: cleanName(in.Name), ContentType: ctype, Purpose: purpose}
	f.StorageKey = tenantID + "/" + f.ID
	if in.UploadedBy != "" {
		f.UploadedBy = &in.UploadedBy
	}
	size, err := s.blobs.Put(ctx, f.StorageKey, io.MultiReader(bytes.NewReader(head), in.Content), s.max)
	if errors.Is(err, storage.ErrTooLarge) {
		return nil, ErrTooLarge
	}
	if err != nil {
		return nil, err
	}
	f.Size = size
	if err := s.repo.Create(ctx, f); err != nil {
		s.dropBlob(ctx, f.StorageKey)
		return nil, err
	}
	return f, nil
}

// List returns the files of the tenant.
func (s *Service) List(ctx context.Context, p core.ListParams) (core.Page[File], error) {
	return s.repo.List(ctx, p)
}

// GetByID returns the metadata of one file.
func (s *Service) GetByID(ctx context.Context, id string) (*File, error) {
	return s.repo.GetByID(ctx, id)
}

// Open returns the metadata and the content of a file. The caller closes the content. A row whose content is
// missing from the storage is logged and answered as not found.
func (s *Service) Open(ctx context.Context, id string) (*File, io.ReadCloser, error) {
	f, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.blobs.Open(ctx, f.StorageKey)
	if errors.Is(err, storage.ErrNotFound) {
		s.log.Error("file content is missing from the storage", "file_id", f.ID, "key", f.StorageKey)
		return nil, nil, core.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return f, rc, nil
}

// Delete removes the row and then the content.
func (s *Service) Delete(ctx context.Context, id string) error {
	f, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.dropBlob(ctx, f.StorageKey)
	return nil
}

// dropBlob removes content that no row points to (any more). A failure only leaves an orphan, which is
// logged: it is not worth failing the request for.
func (s *Service) dropBlob(ctx context.Context, key string) {
	if err := s.blobs.Delete(context.WithoutCancel(ctx), key); err != nil {
		s.log.Error("cannot remove file content", "key", key, "error", err)
	}
}

// cleanName reduces a client-supplied file name to something safe to show and to put in a header:
// no directories, no control characters, at most 255 characters.
func cleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if r := []rune(name); len(r) > maxNameRunes {
		name = string(r[:maxNameRunes])
	}
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	return name
}
