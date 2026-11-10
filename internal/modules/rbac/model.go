// Package rbac decides who may call which endpoint. A user belongs to one group (a role such as "admin"), a group
// is granted endpoints (see package endpoint), and a request is allowed when its group holds a grant for the
// route pattern it matched. Everything else is refused (default deny).
//
// The rules, in order, for a tenant-level token:
//
//  1. the route must be registered in the endpoints table (otherwise refused);
//  2. the endpoint must have scope "tenant";
//  3. the user must still exist and be active (otherwise 401, so a deactivated user is cut off within one cache TTL);
//  4. a public endpoint is allowed to every such user;
//  5. otherwise the user needs a group, and the group needs a grant for the endpoint.
//
// A platform-level token may use "platform" scope endpoints and nothing else. The decision comes from the
// token (level, tenant, user) and the database, never from anything the client sends.
package rbac

import "time"

// GroupEndpoint is one grant: the group may call the endpoint. A grant belongs to a tenant (RLS applies).
type GroupEndpoint struct {
	ID         string     `gorm:"column:group_endpoint_id;primaryKey"`
	Unique     int64      `gorm:"column:group_endpoint_unique;->"`
	TenantID   string     `gorm:"column:tenant_id"`
	GroupID    string     `gorm:"column:group_id"`
	EndpointID string     `gorm:"column:endpoint_id"`
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
	DeletedAt  *time.Time `gorm:"column:deleted_at"`
}

func (*GroupEndpoint) TableName() string { return "group_endpoints" }
