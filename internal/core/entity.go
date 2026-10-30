package core

// Entity is the constraint a model must satisfy to be used with Repository.
// Models are declared with pointer receivers; see internal/modules/group/model.go for an example.
type Entity[T any] interface {
	*T
	TableName() string
	// PrimaryKey returns the primary key column name, e.g. "group_id".
	PrimaryKey() string
	GetID() string
	SetID(string)
}

// TenantOwned marks a model that has a tenant_id column. The repository automatically:
//   - requires a tenant in the context,
//   - adds WHERE tenant_id = ? to every query,
//   - fills in tenant_id on Create.
//
// Models without this method are treated as global tables.
type TenantOwned interface {
	SetTenantID(string)
}
