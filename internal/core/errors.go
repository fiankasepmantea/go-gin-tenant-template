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
)
