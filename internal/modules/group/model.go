// Package group is the "group" business module (roles such as admin, finance) within a single tenant.
package group

import (
	"time"

	"gorm.io/gorm"
)

type Group struct {
	ID          string         `gorm:"column:group_id;primaryKey" json:"group_id"`
	Unique      int64          `gorm:"column:group_unique;->" json:"group_unique"`
	TenantID    string         `gorm:"column:tenant_id" json:"tenant_id"`
	Name        string         `gorm:"column:name" json:"name"`
	Description string         `gorm:"column:description" json:"description"`
	CreatedAt   time.Time      `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (*Group) TableName() string       { return "groups" }
func (*Group) PrimaryKey() string      { return "group_id" }
func (g *Group) GetID() string         { return g.ID }
func (g *Group) SetID(id string)       { g.ID = id }
func (g *Group) SetTenantID(id string) { g.TenantID = id }
