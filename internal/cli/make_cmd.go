package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/generator"
)

func newMakeModuleCmd() *cobra.Command {
	var (
		fields, unique []string
		table, root    string
		dryRun         bool
	)
	cmd := &cobra.Command{
		Use:   "make:module <name>",
		Short: "Generate a module: migration, model, repository, CRUD handler, tests, and register it",
		Long: `Generate a tenant-isolated CRUD module and register it in internal/app/app.go.

Fields are written name:type, or name:type! when the value is required. Types:
string (up to 255 characters), text (up to 5000), int, bigint, float, bool, time, uuid.
--unique makes a required field unique per tenant (among records that are not deleted).

Run it from the project root. It never overwrites a file. After it:
  1. read the generated migration, then apply it (make migrate-up)
  2. make sync-endpoints, then grant the new endpoints to a group (tenantctl group grant --tenant ... --group ...)`,
		Example: `  tenantctl make:module product --field name:string! --field price:bigint! --field active:bool --unique name
  tenantctl make:module category --field name:string! --table categories`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMakeModule(cmd.OutOrStdout(), root, args[0], table, fields, unique, dryRun)
		},
	}
	cmd.Flags().StringArrayVar(&fields, "field", nil, "column as name:type or name:type! (required); repeatable")
	cmd.Flags().StringArrayVar(&unique, "unique", nil, "field that is unique per tenant; repeatable")
	cmd.Flags().StringVar(&table, "table", "", "table name (default: the plural of the module name)")
	cmd.Flags().StringVar(&root, "root", ".", "project root")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be created and change nothing")
	return cmd
}

func runMakeModule(out io.Writer, root, name, table string, fields, unique []string, dryRun bool) error {
	spec, err := generator.NewModuleSpec(name, table, fields, unique)
	if err != nil {
		return err
	}
	modPath, err := generator.ModulePath(root)
	if err != nil {
		return err
	}
	files, err := generator.ModuleFiles(root, spec)
	if err != nil {
		return err
	}
	if c := generator.Conflicts(root, spec, files); len(c) > 0 {
		return fmt.Errorf("nothing was changed; already exists:\n  %s", strings.Join(c, "\n  "))
	}
	// the registration is checked before anything is written, so a failure leaves the tree untouched
	appSrc, err := os.ReadFile(filepath.Join(root, "internal", "app", "app.go")) //nolint:gosec // root is the project directory the developer chose
	if err != nil {
		return err
	}
	if _, err := generator.RegisterModule(appSrc, modPath, spec.Name); err != nil {
		return err
	}
	if dryRun {
		_, _ = fmt.Fprintln(out, "dry run, nothing written. Would create:")
		for _, f := range files {
			_, _ = fmt.Fprintf(out, "  %s\n", f.Path)
		}
		_, _ = fmt.Fprintln(out, "  and register the module in internal/app/app.go")
		return nil
	}
	if err := generator.Write(root, files); err != nil {
		return err
	}
	if err := generator.Register(root, modPath, spec.Name); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "module %q created:\n", spec.Name)
	for _, f := range files {
		_, _ = fmt.Fprintf(out, "  %s\n", f.Path)
	}
	_, _ = fmt.Fprintf(out, "  internal/app/app.go (registered)\n\nNext:\n"+
		"  make migrate-up                 # apply the migration\n"+
		"  make sync-endpoints             # the new routes become known to RBAC (closed until granted)\n"+
		"  tenantctl group grant --tenant <code> --group <group> --all   # or --endpoint \"GET /api/v1/<table>\", see docs/rbac.md\n"+
		"  make test lint\n")
	return nil
}

func newMakeMigrationCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:     "make:migration <name>",
		Short:   "Create the next numbered, empty up/down migration files",
		Example: "  tenantctl make:migration add_price_to_products",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := generator.MigrationFiles(root, args[0])
			if err != nil {
				return err
			}
			if err := generator.Write(root, files); err != nil {
				return err
			}
			for _, f := range files {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", f.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "project root")
	return cmd
}
