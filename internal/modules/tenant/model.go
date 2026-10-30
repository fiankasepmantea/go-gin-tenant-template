// Package tenant is the "tenant" business module. Its table is owned by the platform and is not tenant-scoped.
package tenant

import (
	"time"

	"gorm.io/gorm"
)

const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

// Tenant is a single tenant. Here tenant_id is the primary key, not a filter column.
type Tenant struct {
	ID        string         `gorm:"column:tenant_id;primaryKey" json:"tenant_id"`
	Unique    int64          `gorm:"column:tenant_unique;->" json:"tenant_unique"`
	Code      string         `gorm:"column:code" json:"code"`
	Name      string         `gorm:"column:name" json:"name"`
	Status    string         `gorm:"column:status" json:"status"`
	CreatedAt time.Time      `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (*Tenant) TableName() string  { return "tenants" }
func (*Tenant) PrimaryKey() string { return "tenant_id" }
func (t *Tenant) GetID() string    { return t.ID }
func (t *Tenant) SetID(id string)  { t.ID = id }
