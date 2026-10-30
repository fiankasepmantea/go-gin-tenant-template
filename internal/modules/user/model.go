// Package user is the "user" business module. Platform admins are stored here too
// (Level=platform, TenantID=nil), see docs/adr/0002-platform-admin-and-app-version.md.
package user

import (
	"time"

	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
)

type User struct {
	ID           string         `gorm:"column:user_id;primaryKey" json:"user_id"`
	Unique       int64          `gorm:"column:user_unique;->" json:"user_unique"`
	TenantID     *string        `gorm:"column:tenant_id" json:"tenant_id"`
	Level        string         `gorm:"column:level" json:"level"`
	GroupID      *string        `gorm:"column:group_id" json:"group_id"`
	Username     string         `gorm:"column:username" json:"username"`
	Email        string         `gorm:"column:email" json:"email"`
	Name         string         `gorm:"column:name" json:"name"`
	PasswordHash string         `gorm:"column:password_hash" json:"-"`
	IsActive     bool           `gorm:"column:is_active" json:"is_active"`
	CreatedAt    time.Time      `gorm:"column:created_at" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (*User) TableName() string  { return "users" }
func (*User) PrimaryKey() string { return "user_id" }
func (u *User) GetID() string    { return u.ID }
func (u *User) SetID(id string)  { u.ID = id }

// SetTenantID is called by core.Repository.Create; the level of a regular user is always tenant.
func (u *User) SetTenantID(id string) {
	u.TenantID = &id
	u.Level = auth.LevelTenant
}
