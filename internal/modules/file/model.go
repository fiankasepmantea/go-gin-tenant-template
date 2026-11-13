// Package file is the "file" module: upload, list, download and delete of files of a tenant (avatars, images,
// PDF documents). Content lives in a storage.Store, metadata in the files table.
package file

import (
	"time"

	"gorm.io/gorm"
)

// File is the metadata of one uploaded file. StorageKey is internal and never sent to a client.
type File struct {
	ID          string         `gorm:"column:file_id;primaryKey" json:"file_id"`
	Unique      int64          `gorm:"column:file_unique;->" json:"file_unique"`
	TenantID    string         `gorm:"column:tenant_id" json:"tenant_id"`
	UploadedBy  *string        `gorm:"column:uploaded_by" json:"uploaded_by"`
	Name        string         `gorm:"column:name" json:"name"`
	ContentType string         `gorm:"column:content_type" json:"content_type"`
	Size        int64          `gorm:"column:size" json:"size"`
	Purpose     string         `gorm:"column:purpose" json:"purpose"`
	StorageKey  string         `gorm:"column:storage_key" json:"-"`
	CreatedAt   time.Time      `gorm:"column:created_at" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (*File) TableName() string       { return "files" }
func (*File) PrimaryKey() string      { return "file_id" }
func (f *File) GetID() string         { return f.ID }
func (f *File) SetID(id string)       { f.ID = id }
func (f *File) SetTenantID(id string) { f.TenantID = id }
