package generator

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
)

// Marker is the comment in internal/app/app.go that tells the generator where new modules go.
const Marker = "// tenantctl:modules"

const appFile = "internal/app/app.go"

// RegisterModule adds the module to app.Modules: an import and a line before the marker. It returns the new
// source and does not write it. Registering twice is an error.
func RegisterModule(src []byte, modulePath, name string) ([]byte, error) {
	importLine := fmt.Sprintf("\t%q\n", modulePath+"/internal/modules/"+name)
	call := fmt.Sprintf("\t\t%s.NewHandler(d.DB, chain...),\n", name)
	text := string(src)
	if strings.Contains(text, importLine) || strings.Contains(text, call) {
		return nil, fmt.Errorf("%s already registers the module %q", appFile, name)
	}
	if strings.Count(text, Marker) != 1 {
		return nil, fmt.Errorf("%s must contain exactly one %q comment inside the Modules list; add the module by hand", appFile, Marker)
	}

	lines := strings.SplitAfter(text, "\n")
	var out []string
	prefix := fmt.Sprintf("\t%q", modulePath+"/internal/modules/")
	prefix = prefix[:len(prefix)-1] // drop the closing quote: match the start of the path
	for _, l := range lines {
		if strings.Contains(l, Marker) {
			out = append(out, call)
		}
		out = append(out, l)
	}
	// import: put the line into the block of module imports, keeping the block sorted
	var imp []int
	for i, l := range out {
		if strings.HasPrefix(l, prefix) {
			imp = append(imp, i)
		}
	}
	if len(imp) == 0 {
		return nil, fmt.Errorf("%s has no import of %s/internal/modules/...; add the module by hand", appFile, modulePath)
	}
	at := imp[len(imp)-1] + 1
	for _, i := range imp {
		if strings.TrimSpace(out[i]) > strings.TrimSpace(importLine) {
			at = i
			break
		}
	}
	out = append(out[:at], append([]string{importLine}, out[at:]...)...)

	res, err := format.Source([]byte(strings.Join(out, "")))
	if err != nil {
		return nil, fmt.Errorf("%s would not be valid Go after the change: %w", appFile, err)
	}
	return res, nil
}

// Register patches root/internal/app/app.go in place.
func Register(root, modulePath, name string) error {
	full := filepath.Join(root, appFile)
	src, err := os.ReadFile(full) //nolint:gosec // root is the project directory the developer chose
	if err != nil {
		return err
	}
	res, err := RegisterModule(src, modulePath, name)
	if err != nil {
		return err
	}
	if bytes.Equal(src, res) {
		return nil
	}
	return os.WriteFile(full, res, 0o644) //nolint:gosec // source file
}
