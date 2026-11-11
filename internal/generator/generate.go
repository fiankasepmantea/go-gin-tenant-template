package generator

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

// File is one file to create, with a path relative to the project root.
type File struct {
	Path    string
	Content []byte
}

var migrationRe = regexp.MustCompile(`^(\d+)_.+\.(up|down)\.sql$`)

// NextMigrationNumber returns max(existing)+1 for the migrations in dir, so that files made by hand, by
// `migrate create -seq` and by this generator share one sequence.
func NextMigrationNumber(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", dir, err)
	}
	maxN := 0
	for _, e := range entries {
		m := migrationRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		maxN = max(maxN, n)
	}
	return maxN + 1, nil
}

// ModulePath reads the Go module path from go.mod in root.
func ModulePath(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, "go.mod")) //nolint:gosec // root is the project directory the developer chose
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w (run the command from the project root)", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	return "", fmt.Errorf("go.mod has no module line")
}

var migrationNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,60}$`)

// MigrationFiles makes the up and down files of an empty migration.
func MigrationFiles(root, name string) ([]File, error) {
	if !migrationNameRe.MatchString(name) || strings.HasSuffix(name, "_") || strings.Contains(name, "__") {
		return nil, fmt.Errorf("migration name %q: use lowercase letters, digits and underscores, for example add_price_to_products", name)
	}
	n, err := NextMigrationNumber(filepath.Join(root, "migrations"))
	if err != nil {
		return nil, err
	}
	base := fmt.Sprintf("migrations/%06d_%s", n, name)
	up := "-- " + name + "\n" +
		"-- A new table needs: tenant_id uuid NOT NULL REFERENCES tenants (tenant_id), an index on tenant_id,\n" +
		"-- ENABLE and FORCE ROW LEVEL SECURITY and the tenant_isolation policy (see 000003_create_groups.up.sql).\n" +
		"-- `tenantctl make:module` writes all of that for you.\n"
	down := "-- undo " + name + "\n"
	return []File{{base + ".up.sql", []byte(up)}, {base + ".down.sql", []byte(down)}}, nil
}

type moduleData struct {
	*ModuleSpec
	Path        string // Go module path
	Number      int
	NeedStrings bool // any text field: handler imports strings
	NeedTime    bool
	FilterCols  []string
	SortCols    []string
	UniqueSet   []Field
	HasStrings  bool // some sample value contains %[1]d
	First       Field
	HasRequired bool
}

// ModuleFiles renders every file of a new module. It does not write anything.
func ModuleFiles(root string, s *ModuleSpec) ([]File, error) {
	path, err := ModulePath(root)
	if err != nil {
		return nil, err
	}
	n, err := NextMigrationNumber(filepath.Join(root, "migrations"))
	if err != nil {
		return nil, err
	}
	d := moduleData{ModuleSpec: s, Path: path, Number: n, First: s.Fields[0]}
	for _, f := range s.Fields {
		d.NeedStrings = d.NeedStrings || f.isText()
		d.NeedTime = d.NeedTime || f.Type == TTime
		d.HasStrings = d.HasStrings || f.Type == TString
		d.HasRequired = d.HasRequired || f.Required
		if f.Filterable() {
			d.FilterCols = append(d.FilterCols, f.Name)
		}
		if f.Sortable() {
			d.SortCols = append(d.SortCols, f.Name)
		}
	}
	d.SortCols = append(d.SortCols, "created_at")
	for _, u := range s.Unique {
		f, _ := s.field(u)
		d.UniqueSet = append(d.UniqueSet, f)
	}

	mig := fmt.Sprintf("migrations/%06d_create_%s", n, s.Table)
	dir := "internal/modules/" + s.Name + "/"
	jobs := []struct {
		path, tmpl string
		goCode     bool
	}{
		{mig + ".up.sql", tmplUp, false},
		{mig + ".down.sql", tmplDown, false},
		{dir + "model.go", tmplModel, true},
		{dir + "repository.go", tmplRepository, true},
		{dir + "handler.go", tmplHandler, true},
		{dir + "handler_test.go", tmplHandlerTest, true},
		{"test/integration/" + s.Name + "_test.go", tmplIntegration, true},
	}
	var out []File
	for _, j := range jobs {
		b, err := render(j.path, j.tmpl, d, j.goCode)
		if err != nil {
			return nil, err
		}
		out = append(out, File{j.path, b})
	}
	return out, nil
}

var funcs = template.FuncMap{"quote": strconv.Quote}

func render(name, text string, data any, goCode bool) ([]byte, error) {
	t, err := template.New(name).Funcs(funcs).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	if !goCode {
		return buf.Bytes(), nil
	}
	b, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("generated %s is not valid Go (this is a bug in the generator): %w", name, err)
	}
	return b, nil
}

// Conflicts lists the files that already exist, and a table whose migration exists already.
func Conflicts(root string, s *ModuleSpec, files []File) []string {
	var out []string
	for _, f := range files {
		if strings.HasPrefix(f.Path, "migrations/") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, f.Path)); err == nil {
			out = append(out, f.Path)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(root, "migrations", "*_create_"+s.Table+".*.sql")); len(m) > 0 {
		sort.Strings(m)
		out = append(out, "a migration that creates "+s.Table+": "+filepath.Base(m[0]))
	}
	return out
}

// Write creates the files (directories included). It refuses to overwrite anything.
func Write(root string, files []File) error {
	for _, f := range files {
		full := filepath.Join(root, f.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		fh, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // generated source, not secret
		if err != nil {
			return fmt.Errorf("create %s: %w", f.Path, err)
		}
		if _, err := fh.Write(f.Content); err != nil {
			_ = fh.Close()
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
	}
	return nil
}
