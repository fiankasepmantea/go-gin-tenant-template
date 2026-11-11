package generator

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedModuleCompiles generates a module into a copy of this repository and checks that the result
// builds, passes vet and its own tests. It takes about a minute, so it runs only with GENERATOR_E2E=1
// (CI sets it).
func TestGeneratedModuleCompiles(t *testing.T) {
	if os.Getenv("GENERATOR_E2E") != "1" {
		t.Skip("set GENERATOR_E2E=1 to run")
	}
	src, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() && (rel == ".git" || rel == "bin") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, spec := range []struct {
		name   string
		fields []string
		unique []string
	}{
		{"gen1", []string{"name:string!", "price:bigint!", "active:bool", "note:text", "due:time", "owner:uuid!", "ratio:float", "qty:int"}, []string{"name"}},
		{"gen2", []string{"flag:bool!"}, nil},
	} {
		s, err := NewModuleSpec(spec.name, "", spec.fields, spec.unique)
		if err != nil {
			t.Fatal(err)
		}
		files, err := ModuleFiles(dst, s)
		if err != nil {
			t.Fatal(err)
		}
		if err := Write(dst, files); err != nil {
			t.Fatal(err)
		}
		mod, _ := ModulePath(dst)
		if err := Register(dst, mod, s.Name); err != nil {
			t.Fatal(err)
		}

	}
	for _, args := range [][]string{
		{"build", "./..."},
		{"vet", "./..."},
		{"vet", "-tags", "integration", "./..."},
		{"test", "./internal/modules/gen1/", "./internal/modules/gen2/", "./internal/app/..."},
	} {
		cmd := exec.CommandContext(t.Context(), "go", args...)
		cmd.Dir = dst
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %v: %v\n%s", args, err, out)
		}
	}
}
