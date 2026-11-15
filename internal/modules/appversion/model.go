// Package appversion is the "app version" module: which versions of the client app exist, which one is the
// latest, and the installable package (APK) of each.
//
// The table is global (no tenant_id): one client app serves all tenants. Platform admins manage it
// (/api/v1/app-versions, scope platform); the app itself uses the public routes (/api/v1/public/app-versions)
// to check for updates and to download the package, before anyone has logged in.
package appversion

import (
	"time"

	"gorm.io/gorm"
)

// Platforms a version can be for.
const (
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
	PlatformWeb     = "web"
)

// AppVersion is one released version of the client app for one platform.
type AppVersion struct {
	ID                  string         `gorm:"column:app_version_id;primaryKey" json:"app_version_id"`
	Unique              int64          `gorm:"column:app_version_unique;->" json:"app_version_unique"`
	Platform            string         `gorm:"column:platform" json:"platform"`
	Version             string         `gorm:"column:version" json:"version"`
	MinSupportedVersion string         `gorm:"column:min_supported_version" json:"min_supported_version"`
	IsForceUpdate       bool           `gorm:"column:is_force_update" json:"is_force_update"`
	ReleaseNotes        string         `gorm:"column:release_notes" json:"release_notes"`
	StoreURL            string         `gorm:"column:store_url" json:"store_url"`
	PackageKey          *string        `gorm:"column:package_key" json:"-"`
	PackageSize         *int64         `gorm:"column:package_size" json:"package_size"`
	PackageSHA256       *string        `gorm:"column:package_sha256" json:"package_sha256"`
	CreatedAt           time.Time      `gorm:"column:created_at" json:"created_at"`
	UpdatedAt           time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (*AppVersion) TableName() string  { return "app_versions" }
func (*AppVersion) PrimaryKey() string { return "app_version_id" }
func (a *AppVersion) GetID() string    { return a.ID }
func (a *AppVersion) SetID(id string)  { a.ID = id }

// HasPackage reports whether an installable package was uploaded.
func (a *AppVersion) HasPackage() bool { return a.PackageKey != nil }
