package openapi

import (
	"encoding/json"
	"testing"
	"time"
)

type sample struct {
	Name   string    `json:"name" binding:"required,min=2,max=100"`
	Qty    int       `json:"qty" binding:"gte=0,lte=10"`
	Kind   string    `json:"kind" binding:"oneof=a b"`
	Mail   *string   `json:"mail" binding:"omitempty,email"`
	IDs    []string  `json:"ids" binding:"required,max=5,dive,uuid"`
	Hidden string    `json:"-"`
	NoTag  bool      // named by the Go field
	At     time.Time `json:"at"`
	Seq    int64     `json:"seq" gorm:"column:seq;->"`
	Embed
}

type Embed struct {
	Extra string `json:"extra"`
}

func build(t *testing.T, d *Doc) map[string]any {
	t.Helper()
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	return out
}

func dig(t *testing.T, m any, path ...string) any {
	t.Helper()
	for _, p := range path {
		mm, ok := m.(map[string]any)
		if !ok {
			t.Fatalf("at %q: not an object: %v", p, m)
		}
		if m, ok = mm[p]; !ok {
			t.Fatalf("missing %q", p)
		}
		_ = ok
	}
	return m
}

func TestSchemaFromTags(t *testing.T) {
	d := New("t", "1")
	d.Add(Op{Method: "POST", Path: "/api/v1/things", Body: sample{}, Result: sample{}, Status: 201})
	doc := build(t, d)
	props := dig(t, doc, "components", "schemas", "openapi.sample", "properties").(map[string]any)

	check := func(field, key string, want any) {
		t.Helper()
		got := dig(t, props, field).(map[string]any)[key]
		if w, g := toJSON(want), toJSON(got); w != g {
			t.Errorf("%s.%s = %s, want %s", field, key, g, w)
		}
	}
	check("name", "minLength", 2)
	check("name", "maxLength", 100)
	check("qty", "minimum", 0)
	check("qty", "maximum", 10)
	check("kind", "enum", []string{"a", "b"})
	check("mail", "format", "email")
	check("mail", "nullable", true)
	check("ids", "maxItems", 5)
	check("at", "format", "date-time")
	check("seq", "readOnly", true)
	check("seq", "format", "int64")
	if _, ok := props["extra"]; !ok {
		t.Error("an embedded struct is flattened")
	}
	if _, ok := props["NoTag"]; !ok {
		t.Error("a field without a json tag keeps its Go name")
	}
	if _, ok := props["Hidden"]; ok {
		t.Error(`json:"-" must be skipped`)
	}
	if _, ok := dig(t, props, "ids").(map[string]any)["format"]; ok {
		t.Error("rules after dive belong to the items, not to the array")
	}
	req := toJSON(dig(t, doc, "components", "schemas", "openapi.sample", "required"))
	if req != `["ids","name"]` {
		t.Errorf("required = %s", req)
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestOperation(t *testing.T) {
	d := New("t", "1")
	d.Add(Op{Method: "GET", Path: "/api/v1/things/:id", Summary: "one", Result: sample{}})
	d.Add(Op{Method: "POST", Path: "/api/v1/auth/login", Body: sample{}, Public: true})
	doc := build(t, d)

	get := dig(t, doc, "paths", "/api/v1/things/{id}", "get").(map[string]any)
	if toJSON(get["security"]) == "[]" {
		t.Error("a normal route needs the bearer token")
	}
	if p := dig(t, get, "parameters").([]any)[0].(map[string]any); p["name"] != "id" || p["in"] != "path" || p["schema"].(map[string]any)["format"] != "uuid" {
		t.Errorf("path parameter: %v", p)
	}
	for _, code := range []string{"200", "401", "403", "404", "500", "503"} {
		dig(t, get, "responses", code)
	}
	login := dig(t, doc, "paths", "/api/v1/auth/login", "post").(map[string]any)
	if toJSON(login["security"]) != "[]" {
		t.Error("a public route has empty security")
	}
	resp := login["responses"].(map[string]any)
	if _, ok := resp["401"]; ok {
		t.Error("a public route has no 401")
	}
	dig(t, resp, "422")
	if dig(t, doc, "openapi") != "3.0.3" {
		t.Error("version")
	}
}

func TestListEnvelope(t *testing.T) {
	d := New("t", "1")
	d.Add(Op{Method: "GET", Path: "/api/v1/things", Result: sample{}, Kind: List})
	doc := build(t, d)
	data := dig(t, doc, "paths", "/api/v1/things", "get", "responses", "200", "content", "application/json", "schema", "properties", "data", "properties")
	for _, f := range []string{"total_data", "curr_page", "data_per_page", "data"} {
		dig(t, data, f)
	}
}

func TestRecursiveTypeTerminates(t *testing.T) {
	type node struct {
		Name     string  `json:"name"`
		Children []*node `json:"children"`
	}
	d := New("t", "1")
	d.Add(Op{Method: "GET", Path: "/api/v1/tree", Result: node{}})
	build(t, d)
}

func TestFillRoutesAddsOnlyMissing(t *testing.T) {
	d := New("t", "1")
	d.Add(Op{Method: "GET", Path: "/api/v1/x", Summary: "mine"})
	if !d.Has("get", "/api/v1/x") || d.Has("GET", "/api/v1/y") {
		t.Fatal("Has")
	}
}
