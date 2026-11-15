package appversion

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/storage"
)

// DefaultMaxPackageBytes is the largest package when none is configured.
const DefaultMaxPackageBytes = 200 << 20

var (
	// ErrNoPackage: the version has no package uploaded.
	ErrNoPackage = errors.New("appversion: no package")
	// ErrPackageTooLarge: the package is larger than the limit.
	ErrPackageTooLarge = errors.New("appversion: package too large")
)

// ValidationError is a refused value: the field and a short rule name for the client, a message for people.
type ValidationError struct {
	Field, Rule, Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func invalid(field, rule, msg string) error {
	return &ValidationError{Field: field, Rule: rule, Message: msg}
}

// Store is what the service needs from the data layer. *Repository satisfies it, and a test can pass a fake.
type Store interface {
	Create(ctx context.Context, e *AppVersion) error
	GetByID(ctx context.Context, id string) (*AppVersion, error)
	List(ctx context.Context, p core.ListParams) (core.Page[AppVersion], error)
	Update(ctx context.Context, id string, fields map[string]any) error
	Delete(ctx context.Context, id string) error
	ByPlatform(ctx context.Context, platform string) ([]AppVersion, error)
}

// Options configure the service.
type Options struct {
	MaxPackageBytes int64 // largest package; DefaultMaxPackageBytes when zero
	Log             *slog.Logger
}

// Service holds the rules of app versions: what a version number is, which version is the latest, whether an
// app must update, and that the package is a real APK stored together with its row. The handlers call it.
type Service struct {
	store Store
	blobs storage.Store
	max   int64
	log   *slog.Logger
}

// NewService creates the service.
func NewService(store Store, blobs storage.Store, opt Options) *Service {
	if opt.MaxPackageBytes <= 0 {
		opt.MaxPackageBytes = DefaultMaxPackageBytes
	}
	if opt.Log == nil {
		opt.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, blobs: blobs, max: opt.MaxPackageBytes, log: opt.Log}
}

// MaxPackageBytes is the largest package that is accepted.
func (s *Service) MaxPackageBytes() int64 { return s.max }

// CreateInput describes a new version. MinSupportedVersion empty means every older version is still supported.
type CreateInput struct {
	Platform            string
	Version             string
	MinSupportedVersion string
	IsForceUpdate       bool
	ReleaseNotes        string
	StoreURL            string
}

// Changes are the fields of a version that can change (nil: leave as is). Platform and version are the identity
// of the row and never change: release a new version instead.
type Changes struct {
	MinSupportedVersion *string
	IsForceUpdate       *bool
	ReleaseNotes        *string
	StoreURL            *string
}

func checkPlatform(p string) error {
	switch p {
	case PlatformAndroid, PlatformIOS, PlatformWeb:
		return nil
	}
	return invalid("platform", "oneof", "The platform must be android, ios or web.")
}

func checkStoreURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("store_url", "url", "The store URL must be an http or https address.")
	}
	return nil
}

// Create stores a new version. Version numbers are stored in their canonical form (1.2 becomes 1.2.0), so the
// same version cannot be created twice under two spellings.
func (s *Service) Create(ctx context.Context, in CreateInput) (*AppVersion, error) {
	if err := checkPlatform(in.Platform); err != nil {
		return nil, err
	}
	ver, err := ParseVersion(in.Version)
	if err != nil {
		return nil, invalid("version", "version", "The version must be numbers separated by dots, like 1.4.2.")
	}
	minV := Version{0}
	if strings.TrimSpace(in.MinSupportedVersion) != "" {
		if minV, err = ParseVersion(in.MinSupportedVersion); err != nil {
			return nil, invalid("min_supported_version", "version", "The minimum version must be numbers separated by dots, like 1.4.2.")
		}
	}
	if minV.Compare(ver) > 0 {
		return nil, invalid("min_supported_version", "max", "The minimum supported version cannot be higher than the version.")
	}
	if err := checkStoreURL(in.StoreURL); err != nil {
		return nil, err
	}
	a := &AppVersion{
		Platform: in.Platform, Version: ver.String(), MinSupportedVersion: minV.String(),
		IsForceUpdate: in.IsForceUpdate, ReleaseNotes: in.ReleaseNotes, StoreURL: in.StoreURL,
	}
	if err := s.store.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// Update changes the given fields.
func (s *Service) Update(ctx context.Context, id string, ch Changes) (*AppVersion, error) {
	cur, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if ch.MinSupportedVersion != nil {
		minV, err := ParseVersion(*ch.MinSupportedVersion)
		if err != nil {
			return nil, invalid("min_supported_version", "version", "The minimum version must be numbers separated by dots, like 1.4.2.")
		}
		ver, _ := ParseVersion(cur.Version)
		if minV.Compare(ver) > 0 {
			return nil, invalid("min_supported_version", "max", "The minimum supported version cannot be higher than the version.")
		}
		fields["min_supported_version"] = minV.String()
	}
	if ch.IsForceUpdate != nil {
		fields["is_force_update"] = *ch.IsForceUpdate
	}
	if ch.ReleaseNotes != nil {
		fields["release_notes"] = *ch.ReleaseNotes
	}
	if ch.StoreURL != nil {
		if err := checkStoreURL(*ch.StoreURL); err != nil {
			return nil, err
		}
		fields["store_url"] = *ch.StoreURL
	}
	if err := s.store.Update(ctx, id, fields); err != nil {
		return nil, err
	}
	return s.store.GetByID(ctx, id)
}

// List returns versions, for the platform admins.
func (s *Service) List(ctx context.Context, p core.ListParams) (core.Page[AppVersion], error) {
	return s.store.List(ctx, p)
}

// GetByID returns one version.
func (s *Service) GetByID(ctx context.Context, id string) (*AppVersion, error) {
	return s.store.GetByID(ctx, id)
}

// Delete removes the version and its package.
func (s *Service) Delete(ctx context.Context, id string) error {
	cur, err := s.store.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	if cur.PackageKey != nil {
		s.dropBlob(ctx, *cur.PackageKey)
	}
	return nil
}

// PackageContent is an uploaded file that can be read twice: once to check that it is an APK, once to store it.
type PackageContent interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}

// UploadPackage checks that the content is an APK, stores it and points the version to it. An earlier package of
// the version is replaced. The new content is stored under a new key first and the old one removed after the row
// changed, so a failure at any point leaves the version with a package that works.
func (s *Service) UploadPackage(ctx context.Context, id string, content PackageContent, size int64) (*AppVersion, error) {
	cur, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if cur.Platform != PlatformAndroid {
		return nil, invalid("file", "platform", "Only android versions have a package. Other platforms use the store URL.")
	}
	if size <= 0 {
		return nil, invalid("file", "empty", "The file is empty.")
	}
	if size > s.max {
		return nil, ErrPackageTooLarge
	}
	if err := checkAPK(content, size); err != nil {
		return nil, err
	}
	if _, err := content.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("appversion: rewind: %w", err)
	}

	key := "app-versions/" + cur.ID + "-" + idgen.New()
	sum := sha256.New()
	n, err := s.blobs.Put(ctx, key, io.TeeReader(content, sum), s.max)
	if errors.Is(err, storage.ErrTooLarge) {
		return nil, ErrPackageTooLarge
	}
	if err != nil {
		return nil, err
	}
	err = s.store.Update(ctx, id, map[string]any{
		"package_key": key, "package_size": n, "package_sha256": hex.EncodeToString(sum.Sum(nil)),
	})
	if err != nil {
		s.dropBlob(ctx, key)
		return nil, err
	}
	if cur.PackageKey != nil {
		s.dropBlob(ctx, *cur.PackageKey)
	}
	return s.store.GetByID(ctx, id)
}

// checkAPK accepts a zip archive that has an AndroidManifest.xml: what every APK is. It is not a signature
// check (the phone does that); it keeps other files out of the download.
func checkAPK(r io.ReaderAt, size int64) error {
	notAPK := invalid("file", "type", "The file is not an Android package (APK).")
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return notAPK
	}
	for _, f := range zr.File {
		if f.Name == "AndroidManifest.xml" {
			return nil
		}
	}
	return notAPK
}

// Latest returns the highest version of a platform, by number.
func (s *Service) Latest(ctx context.Context, platform string) (*AppVersion, error) {
	if err := checkPlatform(platform); err != nil {
		return nil, err
	}
	rows, err := s.store.ByPlatform(ctx, platform)
	if err != nil {
		return nil, err
	}
	var best *AppVersion
	var bestV Version
	for i := range rows {
		v, err := ParseVersion(rows[i].Version)
		if err != nil {
			continue // cannot happen for rows made by Create; a hand-made row is skipped, not fatal
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = &rows[i], v
		}
	}
	if best == nil {
		return nil, core.ErrNotFound
	}
	return best, nil
}

// OpenPackage returns the version and the content of its package. The caller closes the content.
func (s *Service) OpenPackage(ctx context.Context, id string) (*AppVersion, io.ReadCloser, error) {
	a, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return s.openPackageOf(ctx, a)
}

// OpenLatestPackage is OpenPackage for the latest version of a platform.
func (s *Service) OpenLatestPackage(ctx context.Context, platform string) (*AppVersion, io.ReadCloser, error) {
	a, err := s.Latest(ctx, platform)
	if err != nil {
		return nil, nil, err
	}
	return s.openPackageOf(ctx, a)
}

func (s *Service) openPackageOf(ctx context.Context, a *AppVersion) (*AppVersion, io.ReadCloser, error) {
	if a.PackageKey == nil {
		return nil, nil, ErrNoPackage
	}
	rc, err := s.blobs.Open(ctx, *a.PackageKey)
	if errors.Is(err, storage.ErrNotFound) {
		s.log.Error("package is missing from the storage", "app_version_id", a.ID, "key", *a.PackageKey)
		return nil, nil, core.ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	return a, rc, nil
}

// CheckResult answers "does this installed version have to update?".
type CheckResult struct {
	Platform            string `json:"platform"`
	CurrentVersion      string `json:"current_version"`
	LatestVersion       string `json:"latest_version"`
	MinSupportedVersion string `json:"min_supported_version"`
	UpdateAvailable     bool   `json:"update_available"`
	ForceUpdate         bool   `json:"force_update"`
	ReleaseNotes        string `json:"release_notes"`
	StoreURL            string `json:"store_url"`
	DownloadURL         string `json:"download_url,omitempty"`
}

// Check compares the version installed on a device with the latest one. An update is forced when the installed
// version is older than the minimum the latest release supports, or when the latest release is marked as a
// forced update and the device is behind it.
func (s *Service) Check(ctx context.Context, platform, current string) (*CheckResult, error) {
	cur, err := ParseVersion(current)
	if err != nil {
		return nil, invalid("version", "version", "The version must be numbers separated by dots, like 1.4.2.")
	}
	latest, err := s.Latest(ctx, platform)
	if err != nil {
		return nil, err
	}
	lv, _ := ParseVersion(latest.Version)
	mv, _ := ParseVersion(latest.MinSupportedVersion)

	behind := cur.Compare(lv) < 0
	res := &CheckResult{
		Platform: platform, CurrentVersion: cur.String(), LatestVersion: latest.Version,
		MinSupportedVersion: latest.MinSupportedVersion, UpdateAvailable: behind,
		ForceUpdate:  cur.Compare(mv) < 0 || (behind && latest.IsForceUpdate),
		ReleaseNotes: latest.ReleaseNotes, StoreURL: latest.StoreURL,
	}
	if latest.HasPackage() {
		res.DownloadURL = DownloadPath(latest.ID)
	}
	return res, nil
}

// DownloadPath is the public path of the package of a version.
func DownloadPath(id string) string { return "/api/v1/public/app-versions/" + id + "/download" }

func (s *Service) dropBlob(ctx context.Context, key string) {
	if err := s.blobs.Delete(context.WithoutCancel(ctx), key); err != nil {
		s.log.Error("cannot remove package content", "key", key, "error", err)
	}
}
