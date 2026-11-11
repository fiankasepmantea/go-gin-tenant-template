package generator

import "fmt"

// base is the Go type of the value without pointer.
func (f Field) base() string {
	switch f.Type {
	case TInt:
		return "int32"
	case TBigint:
		return "int64"
	case TFloat:
		return "float64"
	case TBool:
		return "bool"
	case TTime:
		return "time.Time"
	default: // string, text, uuid
		return "string"
	}
}

func (f Field) isText() bool { return f.Type == TString || f.Type == TText }

// nullable fields are optional values that have no natural "empty" value: they are NULL when not sent.
func (f Field) nullable() bool { return !f.Required && (f.Type == TTime || f.Type == TUUID) }

// ptrInCreate: the create request must tell "not sent" from zero (false, 0), so required numbers use pointers.
func (f Field) ptrInCreate() bool {
	if f.Required {
		return !f.isText() && f.Type != TUUID
	}
	return f.nullable()
}

// GoName is the exported Go field name.
func (f Field) GoName() string { return Pascal(f.Name) }

// ModelType is the type of the model field.
func (f Field) ModelType() string {
	if f.nullable() {
		return "*" + f.base()
	}
	return f.base()
}

// CreateType is the type of the field in the create request.
func (f Field) CreateType() string {
	if f.ptrInCreate() {
		return "*" + f.base()
	}
	return f.base()
}

// UpdateType is always a pointer: nil means "not sent".
func (f Field) UpdateType() string { return "*" + f.base() }

func (f Field) maxTag() string {
	switch f.Type {
	case TString:
		return "max=255"
	case TText:
		return "max=5000"
	}
	return ""
}

// CreateTag is the content of the binding tag for the create request ("" = no tag).
func (f Field) CreateTag() string {
	var parts []string
	if f.Required {
		parts = append(parts, "required")
	} else if f.isText() || f.Type == TUUID {
		parts = append(parts, "omitempty")
	}
	if f.Type == TUUID {
		parts = append(parts, "uuid")
	}
	if m := f.maxTag(); m != "" {
		parts = append(parts, m)
	}
	return join(parts)
}

// UpdateTag is the binding tag for the update request.
func (f Field) UpdateTag() string {
	var parts []string
	if f.isText() || f.Type == TUUID {
		parts = append(parts, "omitempty")
	}
	if f.Type == TUUID {
		parts = append(parts, "uuid")
	}
	if f.Required && f.isText() {
		parts = append(parts, "min=1")
	}
	if m := f.maxTag(); m != "" {
		parts = append(parts, m)
	}
	return join(parts)
}

func join(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// BuildExpr is the Go expression that turns the create request field into the model value.
func (f Field) BuildExpr() string {
	switch {
	case f.isText():
		return "strings.TrimSpace(in." + f.GoName() + ")"
	case f.ptrInCreate() && !f.nullable():
		return "*in." + f.GoName()
	}
	return "in." + f.GoName()
}

// ChangeExpr is the Go expression for the value of a field in a PATCH request.
func (f Field) ChangeExpr() string {
	if f.isText() {
		return "strings.TrimSpace(*in." + f.GoName() + ")"
	}
	return "*in." + f.GoName()
}

// SQLType is the PostgreSQL type.
func (f Field) SQLType() string {
	switch f.Type {
	case TInt:
		return "integer"
	case TBigint:
		return "bigint"
	case TFloat:
		return "double precision"
	case TBool:
		return "boolean"
	case TTime:
		return "timestamptz"
	case TUUID:
		return "uuid"
	}
	return "text"
}

// SQLConstraint is what follows the type in the column definition.
func (f Field) SQLConstraint() string {
	if f.Required {
		return "NOT NULL"
	}
	switch f.Type {
	case TString, TText:
		return "NOT NULL DEFAULT ''"
	case TInt, TBigint, TFloat:
		return "NOT NULL DEFAULT 0"
	case TBool:
		return "NOT NULL DEFAULT false"
	}
	return "" // nullable time, uuid
}

// Filterable: usable as ?field=value and in show_by / all_by.
func (f Field) Filterable() bool {
	switch f.Type {
	case TString, TInt, TBigint, TBool, TUUID:
		return true
	}
	return false
}

// Sortable: usable in ?sort=field.
func (f Field) Sortable() bool {
	switch f.Type {
	case TString, TInt, TBigint, TFloat, TTime:
		return true
	}
	return false
}

// Sample is a JSON value for tests; strings contain %[1]d so every record can be made different.
func (f Field) Sample() string {
	switch f.Type {
	case TString:
		return fmt.Sprintf(`"%s-%%[1]d"`, f.Name)
	case TText:
		return `"some text"`
	case TInt, TBigint:
		return "1"
	case TFloat:
		return "1.5"
	case TBool:
		return "false"
	case TTime:
		return `"2026-01-02T03:04:05Z"`
	}
	return `"11111111-1111-4111-8111-111111111111"`
}

// Alt is a second JSON value, different from Sample, for the update test.
func (f Field) Alt() string {
	switch f.Type {
	case TString:
		return `"changed"`
	case TText:
		return `"other text"`
	case TInt, TBigint:
		return "2"
	case TFloat:
		return "2.5"
	case TBool:
		return "true"
	case TTime:
		return `"2027-01-02T03:04:05Z"`
	}
	return `"22222222-2222-4222-8222-222222222222"`
}
