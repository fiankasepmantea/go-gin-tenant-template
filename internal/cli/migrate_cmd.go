package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/migrate"
)

// target is one database a migrate command works on.
type target struct {
	name string
	db   *gorm.DB
}

// openTargets connects to the databases the flags select and returns a function that closes them. Tests replace it.
var openTargets = func(ref string, all bool) ([]target, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	control, err := connectControl(cfg)
	if err != nil {
		return nil, nil, err
	}
	router := newRouter(cfg, control)
	closeAll := func() { router.Close(); closeDB(control) }

	var out []target
	switch {
	case ref != "":
		db, err := router.Dedicated(ref)
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		out = append(out, target{"ref " + ref, db})
	case all:
		out = append(out, target{"shared", control})
		for _, r := range router.Refs() {
			db, err := router.Dedicated(r)
			if err != nil {
				closeAll()
				return nil, nil, err
			}
			out = append(out, target{"ref " + r, db})
		}
	default:
		out = append(out, target{"shared", control})
	}
	return out, closeAll, nil
}

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply the SQL migrations to the shared and the dedicated databases",
		Long: "Applies migrations/*.up.sql in order, each file in its own transaction, under a lock. It uses the same\n" +
			"schema_migrations table as golang-migrate, so `make migrate-up` and this command can be mixed.\n" +
			"Without flags it works on the shared database (DB_*); --ref on one dedicated database; --all on all of them.",
	}
	var ref string
	var all bool
	cmd.PersistentFlags().StringVar(&ref, "ref", "", "reference of a dedicated database (TENANT_DB_URL_<REF>)")
	cmd.PersistentFlags().BoolVar(&all, "all", false, "the shared database and every configured dedicated database")

	run := func(fn func(ctx context.Context, w io.Writer, t target) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			if ref != "" && all {
				return errors.New("use --ref or --all, not both")
			}
			if ref != "" && !config.ValidTenantDBRef(ref) {
				return fmt.Errorf("db ref %q is not valid: %s", ref, config.TenantDBRefPattern)
			}
			ts, closeFn, err := openTargets(ref, all)
			if err != nil {
				return err
			}
			defer closeFn()
			var errs []error
			for _, t := range ts {
				if err := fn(cmd.Context(), cmd.OutOrStdout(), t); err != nil {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: FAILED: %v\n", t.name, err)
					errs = append(errs, fmt.Errorf("%s: %w", t.name, err))
				}
			}
			return errors.Join(errs...)
		}
	}
	ctx := func(c context.Context) context.Context {
		if c == nil {
			return context.Background()
		}
		return c
	}

	up := &cobra.Command{
		Use:   "up",
		Short: "Apply every pending migration",
		Args:  cobra.NoArgs,
		RunE: run(func(c context.Context, w io.Writer, t target) error {
			applied, err := migrate.Up(ctx(c), t.db, migrationsDir)
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				_, _ = fmt.Fprintf(w, "%s: up to date\n", t.name)
				return nil
			}
			_, _ = fmt.Fprintf(w, "%s: applied %s\n", t.name, versions(applied))
			return nil
		}),
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the version of each database",
		Args:  cobra.NoArgs,
		RunE: run(func(c context.Context, w io.Writer, t target) error {
			all, err := migrate.Load(migrationsDir)
			if err != nil {
				return err
			}
			st, err := migrate.Current(ctx(c), t.db)
			if err != nil {
				return err
			}
			note := ""
			switch {
			case st.Dirty:
				note = " DIRTY"
			case st.Version == 0 && st.HasData:
				note = " (tables exist but no history: use baseline)"
			case st.Version == migrate.Latest(all):
				note = " (up to date)"
			default:
				note = fmt.Sprintf(" (%d pending)", len(migrate.Pending(all, st)))
			}
			_, _ = fmt.Fprintf(w, "%s: version %d of %d%s\n", t.name, st.Version, migrate.Latest(all), note)
			return nil
		}),
	}
	var version int64
	baseline := &cobra.Command{
		Use:   "baseline",
		Short: "Record the version of a database whose migrations were applied by hand",
		Long: "For a database that has the tables but no schema_migrations (migrations were applied with psql): records\n" +
			"that it is at --version without running anything. Refuses a database that already has a history.",
		Args: cobra.NoArgs,
		RunE: run(func(c context.Context, w io.Writer, t target) error {
			if err := migrate.Baseline(ctx(c), t.db, migrationsDir, version); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "%s: recorded at version %d\n", t.name, version)
			return nil
		}),
	}
	baseline.Flags().Int64Var(&version, "version", 0, "number of the last migration that was applied (required)")
	_ = baseline.MarkFlagRequired("version")

	cmd.AddCommand(up, status, baseline)
	return cmd
}

func versions(v []int64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprintf("%06d", x)
	}
	return strings.Join(s, ", ")
}
