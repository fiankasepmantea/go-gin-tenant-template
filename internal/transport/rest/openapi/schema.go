package openapi

import (
	"path"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// schemas turns Go types into JSON schema components, reading the json and binding tags.
type schemas struct {
	defs  map[string]any
	names map[reflect.Type]string
}

func newSchemas() *schemas {
	s := &schemas{defs: map[string]any{}, names: map[reflect.Type]string{}}
	s.defs["ErrorBody"] = map[string]any{"type": "object", "properties": map[string]any{
		"success": map[string]any{"type": "boolean", "example": false},
		"code":    map[string]any{"type": "integer", "description": "the HTTP status"},
		"error": map[string]any{"type": "object", "properties": map[string]any{
			"code":    map[string]any{"type": "string", "description": "stable machine-readable code, see docs/http-api.md"},
			"message": map[string]any{"type": "string"},
			"details": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"field": map[string]any{"type": "string", "description": "the JSON name of the field"},
				"rule":  map[string]any{"type": "string", "description": "the rule that failed, e.g. required, max"},
			}}},
		}},
	}}
	return s
}

func (s *schemas) components() map[string]any {
	return map[string]any{"schemas": s.defs}
}

var timeType = reflect.TypeOf(time.Time{})

// of returns the schema of the type of v.
func (s *schemas) of(v any) map[string]any {
	if t, ok := v.(reflect.Type); ok {
		return s.typ(t)
	}
	return s.typ(reflect.TypeOf(v))
}

func (s *schemas) typ(t reflect.Type) map[string]any {
	nullable := false
	for t.Kind() == reflect.Pointer {
		t, nullable = t.Elem(), true
	}
	out := s.base(t)
	if nullable {
		if _, isRef := out["$ref"]; isRef {
			return map[string]any{"allOf": []any{out}, "nullable": true}
		}
		out["nullable"] = true
	}
	return out
}

func (s *schemas) base(t reflect.Type) map[string]any {
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return map[string]any{"type": "integer"}
	case reflect.Int64, reflect.Uint64:
		return map[string]any{"type": "integer", "format": "int64"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return map[string]any{"type": "integer", "minimum": 0}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": s.typ(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": s.typ(t.Elem())}
	case reflect.Struct:
		return s.structRef(t)
	}
	return map[string]any{} // interface{} and the rest: anything
}

func (s *schemas) structRef(t reflect.Type) map[string]any {
	name, ok := s.names[t]
	if !ok {
		// package and type: two modules both have a createInput, and the name must not depend on the order
		// in which the operations are visited
		name = t.Name()
		if name == "" {
			name = "Object"
		}
		if pkg := path.Base(t.PkgPath()); pkg != "." && pkg != "" {
			name = pkg + "." + name
		}
		base := name
		for i := 2; ; i++ {
			if _, taken := s.defs[name]; !taken {
				break
			}
			name = base + strconv.Itoa(i)
		}
		s.names[t] = name
		s.defs[name] = map[string]any{} // placeholder: stops recursion
		s.defs[name] = s.object(t)
	}
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func (s *schemas) object(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := map[string]bool{}
	s.fields(t, props, required)
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = sortedKeys(required)
	}
	return out
}

func (s *schemas) fields(t reflect.Type, props map[string]any, required map[string]bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		jsonTag := f.Tag.Get("json")
		name, _, _ := strings.Cut(jsonTag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				s.fields(ft, props, required)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		sc := s.typ(f.Type)
		if strings.Contains(f.Tag.Get("gorm"), "->") && !strings.Contains(f.Tag.Get("gorm"), "->:false") {
			sc = withKey(sc, "readOnly", true)
		}
		rules := f.Tag.Get("binding")
		if rules != "" {
			var req bool
			sc, req = applyRules(sc, f.Type, rules)
			if req {
				required[name] = true
			}
		}
		props[name] = sc
	}
}

func withKey(m map[string]any, k string, v any) map[string]any {
	if _, isRef := m["$ref"]; isRef {
		return map[string]any{"allOf": []any{m}, k: v}
	}
	m[k] = v
	return m
}

// applyRules maps the binding tag of go-playground/validator to JSON schema keywords. Unknown rules are ignored.
func applyRules(sc map[string]any, t reflect.Type, rules string) (map[string]any, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	isStr := t.Kind() == reflect.String
	isArr := t.Kind() == reflect.Slice || t.Kind() == reflect.Array
	req := false
	for _, r := range strings.Split(rules, ",") {
		key, val, _ := strings.Cut(r, "=")
		num, numErr := strconv.ParseFloat(val, 64)
		set := func(str, arr, n string) {
			if numErr != nil {
				return
			}
			switch {
			case isStr && str != "":
				sc = withKey(sc, str, int(num))
			case isArr && arr != "":
				sc = withKey(sc, arr, int(num))
			case !isStr && !isArr && n != "":
				sc = withKey(sc, n, num)
			}
		}
		if key == "dive" {
			break // the rules after dive are for the items
		}
		switch key {
		case "required":
			req = true
		case "min":
			set("minLength", "minItems", "minimum")
		case "max":
			set("maxLength", "maxItems", "maximum")
		case "gte":
			set("", "", "minimum")
		case "lte":
			set("", "", "maximum")
		case "gt":
			if numErr == nil && !isStr && !isArr {
				sc = withKey(withKey(sc, "minimum", num), "exclusiveMinimum", true)
			}
		case "lt":
			if numErr == nil && !isStr && !isArr {
				sc = withKey(withKey(sc, "maximum", num), "exclusiveMaximum", true)
			}
		case "len":
			set("minLength", "minItems", "")
			set("maxLength", "maxItems", "")
		case "email":
			sc = withKey(sc, "format", "email")
		case "uuid", "uuid4":
			sc = withKey(sc, "format", "uuid")
		case "oneof":
			var vals []any
			for _, v := range strings.Fields(val) {
				if isStr {
					vals = append(vals, v)
				} else if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					vals = append(vals, n)
				}
			}
			if len(vals) > 0 {
				sc = withKey(sc, "enum", vals)
			}
		}
	}
	return sc, req
}
