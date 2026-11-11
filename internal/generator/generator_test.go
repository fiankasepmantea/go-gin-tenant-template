package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluralize(t *testing.T) {
	for in, want := range map[string]string{
		"product": "products", "category": "categories", "box": "boxes", "bus": "buses",
		"branch": "branches", "day": "days", "dish": "dishes",
	} {
		if got := Pluralize(in); got != want {
			t.Errorf("Pluralize(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestPascal(t *testing.T) {
	for in, want := range map[string]string{
		"name": "Name", "unit_price": "UnitPrice", "image_url": "ImageURL", "sku": "SKU", "owner_id": "OwnerID",
	} {
		if got := Pascal(in); got != want {
			t.Errorf("Pascal(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestParseField(t *testing.T) {
	f, err := ParseField("price:bigint!")
	if err != nil || f != (Field{Name: "price", Type: TBigint, Required: true}) {
		t.Fatalf("got %+v %v", f, err)
	}
	for _, bad := range []string{"price", "Price:int", "price:money", "1a:int", "a__b:int", "a_:int", ":int", "a:"} {
		if _, err := ParseField(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestNewModuleSpecRejects(t *testing.T) {
	ok := []string{"name:string!"}
	cases := map[string]struct {
		name, table string
		fields      []string
		unique      []string
	}{
		"upper case name":      {"Product", "", ok, nil},
		"too short":            {"p", "", ok, nil},
		"digit first":          {"1abc", "", ok, nil},
		"go keyword":           {"func", "", ok, nil},
		"predeclared":          {"string", "", ok, nil},
		"template package":     {"user", "", ok, nil},
		"reserved table":       {"item", "users", ok, nil},
		"no fields":            {"item", "", nil, nil},
		"duplicate field":      {"item", "", []string{"a:int", "a:string"}, nil},
		"template column":      {"item", "", []string{"tenant_id:uuid"}, nil},
		"own id column":        {"item", "", []string{"item_id:uuid"}, nil},
		"sql keyword":          {"item", "", []string{"order:int"}, nil},
		"model method clash":   {"item", "", []string{"table_name:string"}, nil},
		"same go name":         {"item", "", []string{"url:string", "u_r_l:string"}, nil},
		"unique unknown":       {"item", "", ok, []string{"nope"}},
		"unique optional":      {"item", "", []string{"name:string"}, []string{"name"}},
		"unique on text":       {"item", "", []string{"body:text!"}, []string{"body"}},
		"unique twice":         {"item", "", ok, []string{"name", "name"}},
		"too many fields":      {"item", "", manyFields(31), nil},
		"identifier too long":  {"item", strings.Repeat("t", 50), []string{strings.Repeat("c", 40) + ":string!"}, []string{strings.Repeat("c", 40)}},
		"table with bad chars": {"item", "Items", ok, nil},
	}
	for label, c := range cases {
		if _, err := NewModuleSpec(c.name, c.table, c.fields, c.unique); err == nil {
			t.Errorf("%s: must be rejected", label)
		}
	}
	s, err := NewModuleSpec("category", "", ok, []string{"name"})
	if err != nil || s.Table != "categories" || s.IDColumn() != "category_id" || s.Type() != "Category" {
		t.Fatalf("valid spec: %+v %v", s, err)
	}
}

func manyFields(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "f" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ":int"
	}
	return out
}

// project makes a minimal project tree: go.mod, migrations and an app.go like the real one.
func project(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(p, s string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/shop\n\ngo 1.25\n")
	write("migrations/000001_a.up.sql", "")
	write("migrations/000001_a.down.sql", "")
	write("migrations/000007_b.up.sql", "")
	write("migrations/README.md", "")
	real, err := os.ReadFile("../app/app.go")
	if err != nil {
		t.Fatal(err)
	}
	write("internal/app/app.go", strings.ReplaceAll(string(real), "github.com/fiankasepmantea/go-gin-tenant-template", "example.com/shop"))
	return root
}

func TestNextMigrationNumberAndMigrationFiles(t *testing.T) {
	root := project(t)
	n, err := NextMigrationNumber(filepath.Join(root, "migrations"))
	if err != nil || n != 8 {
		t.Fatalf("next = %d %v, want 8", n, err)
	}
	files, err := MigrationFiles(root, "add_price")
	if err != nil || len(files) != 2 || files[0].Path != "migrations/000008_add_price.up.sql" || files[1].Path != "migrations/000008_add_price.down.sql" {
		t.Fatalf("files = %+v %v", files, err)
	}
	for _, bad := range []string{"Add", "a-b", "a__b", "x_", "1x", ""} {
		if _, err := MigrationFiles(root, bad); err == nil {
			t.Errorf("name %q must be rejected", bad)
		}
	}
}

func TestModulePath(t *testing.T) {
	root := project(t)
	if p, err := ModulePath(root); err != nil || p != "example.com/shop" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := ModulePath(t.TempDir()); err == nil {
		t.Fatal("no go.mod must be an error")
	}
}

func TestRegisterModuleKeepsImportsSortedAndRefusesTwice(t *testing.T) {
	root := project(t)
	src, _ := os.ReadFile(filepath.Join(root, "internal/app/app.go"))
	out, err := RegisterModule(src, "example.com/shop", "invoice")
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, "\t\tinvoice.NewHandler(d.DB, chain...),\n\t\t"+Marker) {
		t.Errorf("handler line is not right above the marker:\n%s", got)
	}
	iInv := strings.Index(got, `"example.com/shop/internal/modules/invoice"`)
	iEnd := strings.Index(got, `"example.com/shop/internal/modules/group"`)
	iRbac := strings.Index(got, `"example.com/shop/internal/modules/rbac"`)
	if iInv < iEnd || iInv > iRbac {
		t.Errorf("import is not sorted: group=%d invoice=%d rbac=%d", iEnd, iInv, iRbac)
	}
	if _, err := RegisterModule(out, "example.com/shop", "invoice"); err == nil {
		t.Error("registering twice must fail")
	}
	// a module that sorts last, and one that sorts first
	for _, n := range []string{"zebra", "aardvark"} {
		if out, err = RegisterModule(out, "example.com/shop", n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RegisterModule([]byte(strings.ReplaceAll(string(src), Marker, "")), "example.com/shop", "x"); err == nil {
		t.Error("without the marker the generator must refuse")
	}
}

func TestModuleFilesRenderValidGoForEveryType(t *testing.T) {
	root := project(t)
	for _, c := range []struct {
		fields []string
		unique []string
	}{
		{[]string{"a:string!", "b:text", "c:int", "d:bigint!", "e:float!", "f:bool", "g:time", "h:uuid", "i:time!", "j:uuid!", "k:string"}, []string{"a", "d", "i", "j"}},
		{[]string{"qty:int"}, nil},
		{[]string{"ratio:float", "flag:bool!"}, nil},
		{[]string{"seen_at:time!"}, []string{"seen_at"}},
	} {
		s, err := NewModuleSpec("thing", "", c.fields, c.unique)
		if err != nil {
			t.Fatal(err)
		}
		files, err := ModuleFiles(root, s) // gofmt-valid Go or an error
		if err != nil {
			t.Fatalf("%v: %v", c.fields, err)
		}
		if len(files) != 7 || files[0].Path != "migrations/000008_create_things.up.sql" {
			t.Fatalf("files: %+v", files)
		}
	}
}

func TestMigrationHasTenantIsolation(t *testing.T) {
	root := project(t)
	s, _ := NewModuleSpec("product", "", []string{"name:string!", "code:uuid!", "n:int"}, []string{"name", "code"})
	files, err := ModuleFiles(root, s)
	if err != nil {
		t.Fatal(err)
	}
	up := string(files[0].Content)
	for _, want := range []string{
		"tenant_id uuid NOT NULL REFERENCES tenants (tenant_id)",
		"UNIQUE (tenant_id, product_id)",
		"CREATE INDEX products_tenant_idx ON products (tenant_id)",
		"products_tenant_name_uq ON products (tenant_id, lower(name)) WHERE deleted_at IS NULL",
		"products_tenant_code_uq ON products (tenant_id, code) WHERE deleted_at IS NULL",
		"n integer NOT NULL DEFAULT 0",
		"EXECUTE FUNCTION set_updated_at()",
		"ENABLE ROW LEVEL SECURITY", "FORCE ROW LEVEL SECURITY",
		"USING (tenant_id = app_tenant())", "WITH CHECK (tenant_id = app_tenant())",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("migration lacks %q:\n%s", want, up)
		}
	}
	if string(files[1].Content) != "DROP TABLE IF EXISTS products;\n" {
		t.Errorf("down = %q", files[1].Content)
	}
}

func TestConflictsAndWriteNeverOverwrite(t *testing.T) {
	root := project(t)
	s, _ := NewModuleSpec("product", "", []string{"name:string!"}, nil)
	files, _ := ModuleFiles(root, s)
	if c := Conflicts(root, s, files); len(c) != 0 {
		t.Fatalf("fresh project: %v", c)
	}
	if err := Write(root, files); err != nil {
		t.Fatal(err)
	}
	if c := Conflicts(root, s, files); len(c) == 0 {
		t.Fatal("second run must see the existing files")
	}
	if err := Write(root, files[2:3]); err == nil {
		t.Fatal("Write must not overwrite")
	}
	// a hand-made migration for the table is a conflict too
	if err := os.WriteFile(filepath.Join(root, "migrations", "000020_create_things.up.sql"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s3, _ := NewModuleSpec("thing", "", []string{"name:string!"}, nil)
	f3, _ := ModuleFiles(root, s3)
	if c := Conflicts(root, s3, f3); len(c) != 1 || !strings.Contains(c[0], "things") {
		t.Fatalf("conflicts: %v", c)
	}
}
