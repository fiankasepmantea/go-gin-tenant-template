package session

import (
	"time"

	"gorm.io/gorm"
)

// UserToken is one login session, identified by its refresh token. Only the SHA-256 hash of the token is stored.
// The access token carries this row's ID as its jti, which ties the two together.
type UserToken struct {
	ID        string         `gorm:"column:user_token_id;primaryKey"`
	Unique    int64          `gorm:"column:user_token_unique;->"`
	TenantID  *string        `gorm:"column:tenant_id"` // nil for platform admins
	UserID    string         `gorm:"column:user_id"`
	TokenHash []byte         `gorm:"column:token_hash"`
	UserAgent string         `gorm:"column:user_agent"`
	IPAddress string         `gorm:"column:ip_address"`
	ExpiresAt time.Time      `gorm:"column:expires_at"`
	RevokedAt *time.Time     `gorm:"column:revoked_at"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index"`
}

func (*UserToken) TableName() string { return "user_tokens" }
