package core

import "errors"

var (
	// ErrNotFound: the record does not exist, or it exists but belongs to another tenant (deliberately not distinguished).
	ErrNotFound = errors.New("record not found")
	// ErrNoTenant: an operation on a tenant-scoped table was called without a tenant in the context.
	ErrNoTenant = errors.New("no tenant in context")
	// ErrNoFields: Update was called without any columns.
	ErrNoFields = errors.New("no columns to update")
	// ErrProtectedField: identity/tenant columns must not be changed through Update.
	ErrProtectedField = errors.New("this column must not be modified")
	// ErrConflict: a unique constraint was violated (the record already exists).
	ErrConflict = errors.New("record already exists")
	// ErrReference: a foreign key points to a record that does not exist (or is not visible to this tenant).
	ErrReference = errors.New("referenced record does not exist")
	// ErrInvalidInput: the database rejected a value (wrong type, NOT NULL, CHECK constraint).
	ErrInvalidInput = errors.New("invalid value")
	// ErrInvalidColumn: a column name used for filtering or sorting is not a plain identifier.
	ErrInvalidColumn = errors.New("invalid column name")
)
