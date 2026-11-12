package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/app"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/config"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/database"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
)

// openProvisioner connects to the database from the active .env file. Tests replace it.
var openProvisioner = func() (*Provisioner, func(), error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	db, err := database.Connect(cfg.DB, cfg.IsProduction())
	if err != nil {
		return nil, nil, err
	}
	hasher, err := password.New(password.DefaultParams())
	if err != nil {
		closeDB(db)
		return nil, nil, err
	}
	router := newRouter(cfg, db)
	return NewProvisionerWithRouter(router, hasher, migrationsDir), func() { router.Close(); closeDB(db) }, nil
}

// migrationsDir is where the up files are, relative to the directory the command runs in (the project root).
const migrationsDir = "migrations"

func newRouter(cfg *config.Config, control *gorm.DB) *database.Router {
	return database.NewRouter(database.RouterConfig{
		Control: control, URLs: cfg.TenantDB.URLs, Production: cfg.IsProduction(),
		Pool: database.Pool{MaxOpen: cfg.TenantDB.MaxOpenConns, MaxIdle: cfg.TenantDB.MaxIdleConns, MaxLifetime: cfg.DB.ConnMaxLifetime},
	})
}

func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// readPassword reads the password from the first line of stdin. Passwords are never taken from flags:
// flags end up in shell history and in the process list.
func readPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	pw := strings.TrimRight(line, "\r\n")
	if pw == "" {
		return "", errors.New("no password on stdin (pipe it in, see --help)")
	}
	return pw, nil
}

const stdinHelp = `
The password is read from stdin, never from a flag. Examples:

  read -rsp "Password: " PW; echo; printf '%%s\n' "$PW" | tenantctl %s ... --password-stdin; unset PW
`

func newTenantCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tenant", Short: "Manage tenants"}

	var code, name, dbRef string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a tenant",
		Long: "Create a tenant. By default its data lives in the shared database. With --db-ref it gets a dedicated\n" +
			"database: set TENANT_DB_URL_<REF> and run `tenantctl migrate up --ref <ref>` first. Only new tenants can\n" +
			"be placed in a dedicated database (see docs/database-per-tenant.md).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			t, err := p.CreateTenantIn(context.Background(), code, name, dbRef)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "tenant created: code=%s id=%s\n", t.Code, t.ID)
			return nil
		},
	}
	create.Flags().StringVar(&code, "code", "", "tenant code used at login, e.g. acme (required)")
	create.Flags().StringVar(&name, "name", "", "display name (required)")
	create.Flags().StringVar(&dbRef, "db-ref", "", "reference of a dedicated database (TENANT_DB_URL_<REF>); empty: shared database")
	_ = create.MarkFlagRequired("code")
	_ = create.MarkFlagRequired("name")

	cmd.AddCommand(create)
	return cmd
}

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage tenant users"}

	var tenantCode, username, email, name, groupName string
	var stdin bool
	create := &cobra.Command{
		Use:     "create",
		Short:   "Create a user in a tenant",
		Long:    "Create an active user in an existing tenant." + fmt.Sprintf(stdinHelp, "user create"),
		Args:    cobra.NoArgs,
		Example: "  tenantctl user create --tenant acme --username admin --email admin@acme.test --name \"Acme Admin\" --password-stdin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdin {
				return errors.New("--password-stdin is required")
			}
			pw, err := readPassword(cmd.InOrStdin())
			if err != nil {
				return err
			}
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			u, err := p.CreateTenantUser(context.Background(), tenantCode, username, email, name, pw)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "user created: tenant=%s username=%s id=%s\n", tenantCode, u.Username, u.ID)
			if groupName != "" {
				if err := p.SetUserGroup(context.Background(), tenantCode, u.Username, groupName); err != nil {
					return fmt.Errorf("the user was created, but putting it in the group failed: %w", err)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "user %s is now in group %s\n", u.Username, groupName)
			}
			return nil
		},
	}
	create.Flags().StringVar(&tenantCode, "tenant", "", "tenant code (required)")
	create.Flags().StringVar(&username, "username", "", "login name (required)")
	create.Flags().StringVar(&email, "email", "", "email address (required)")
	create.Flags().StringVar(&name, "name", "", "display name (required)")
	create.Flags().StringVar(&groupName, "group", "", "put the user in this group (it must exist, see `group create`)")
	create.Flags().BoolVar(&stdin, "password-stdin", false, "read the password from stdin (required)")
	for _, f := range []string{"tenant", "username", "email", "name"} {
		_ = create.MarkFlagRequired(f)
	}

	var setTenant, setUser, setGroup string
	setGroupCmd := &cobra.Command{
		Use:     "set-group",
		Short:   "Put an existing user into a group",
		Args:    cobra.NoArgs,
		Example: "  tenantctl user set-group --tenant acme --username admin --group admin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			if err := p.SetUserGroup(context.Background(), setTenant, setUser, setGroup); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "user %s is now in group %s\n", setUser, setGroup)
			return nil
		},
	}
	setGroupCmd.Flags().StringVar(&setTenant, "tenant", "", "tenant code (required)")
	setGroupCmd.Flags().StringVar(&setUser, "username", "", "login name (required)")
	setGroupCmd.Flags().StringVar(&setGroup, "group", "", "group name (required)")
	for _, f := range []string{"tenant", "username", "group"} {
		_ = setGroupCmd.MarkFlagRequired(f)
	}

	cmd.AddCommand(create, setGroupCmd)
	return cmd
}

func newGroupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Manage groups (roles) and their permissions"}

	var tenantCode, name, description string
	create := &cobra.Command{
		Use:     "create",
		Short:   "Create a group in a tenant (it starts without permissions)",
		Args:    cobra.NoArgs,
		Example: "  tenantctl group create --tenant acme --name admin --description \"Full access\"",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			g, err := p.CreateGroup(context.Background(), tenantCode, name, description)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "group created: tenant=%s name=%s id=%s\n", tenantCode, g.Name, g.ID)
			return nil
		},
	}
	create.Flags().StringVar(&tenantCode, "tenant", "", "tenant code (required)")
	create.Flags().StringVar(&name, "name", "", "group name (required)")
	create.Flags().StringVar(&description, "description", "", "what the group is for")
	_ = create.MarkFlagRequired("tenant")
	_ = create.MarkFlagRequired("name")

	var gTenant, gGroup string
	var all bool
	var specs []string
	grant := &cobra.Command{
		Use:   "grant",
		Short: "Grant endpoints to a group (adds to what the group already has)",
		Long: "Grant endpoints to a group. Run `tenantctl endpoint sync` first so that the endpoints exist.\n" +
			"Use --all for every endpoint that can be granted, or --endpoint \"METHOD /route/pattern\" (repeatable).",
		Args:    cobra.NoArgs,
		Example: "  tenantctl group grant --tenant acme --group admin --all\n  tenantctl group grant --tenant acme --group viewer --endpoint \"GET /api/v1/groups\"",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			n, err := p.GrantEndpoints(context.Background(), gTenant, gGroup, all, specs)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "group %s now has %d endpoints (a running server sees this within the cache time, about 30 seconds)\n", gGroup, n)
			return nil
		},
	}
	grant.Flags().StringVar(&gTenant, "tenant", "", "tenant code (required)")
	grant.Flags().StringVar(&gGroup, "group", "", "group name (required)")
	grant.Flags().BoolVar(&all, "all", false, "grant every endpoint that can be granted")
	grant.Flags().StringArrayVar(&specs, "endpoint", nil, "\"METHOD /route/pattern\" to grant (repeatable)")
	_ = grant.MarkFlagRequired("tenant")
	_ = grant.MarkFlagRequired("group")

	cmd.AddCommand(create, grant)
	return cmd
}

func newEndpointCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "endpoint", Short: "Manage the endpoint whitelist"}

	var prune bool
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Make the endpoints table match the routes of the server",
		Long: "Reads the routes of the API from the code and adds the missing ones to the endpoints table, without any\n" +
			"grants (a new route is refused until a group is granted it). Run it after every deploy that adds or\n" +
			"changes routes, before the new version takes traffic.\n" +
			"Rows whose route no longer exists are reported; --prune soft-deletes them.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			routes := app.Routes(app.Modules(app.Deps{}))
			res, err := p.SyncEndpoints(context.Background(), routes, prune)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "endpoints: %d routes, %d added, %d updated\n", res.Total, res.Inserted, res.Updated)
			for _, s := range res.Stale {
				_, _ = fmt.Fprintf(out, "no longer in the code: %s\n", s)
			}
			if prune {
				_, _ = fmt.Fprintf(out, "pruned: %d\n", res.Pruned)
			} else if len(res.Stale) > 0 {
				_, _ = fmt.Fprintln(out, "(kept; run with --prune to remove them)")
			}
			return nil
		},
	}
	sync.Flags().BoolVar(&prune, "prune", false, "soft-delete endpoints whose route no longer exists")

	cmd.AddCommand(sync)
	return cmd
}

func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "admin", Short: "Manage platform admins (the \"building owners\")"}

	var username, email, name string
	var stdin bool
	create := &cobra.Command{
		Use:     "create",
		Short:   "Create a platform admin",
		Long:    "Create a platform admin, who belongs to no tenant and logs in at /api/v1/auth/platform/login." + fmt.Sprintf(stdinHelp, "admin create"),
		Args:    cobra.NoArgs,
		Example: "  tenantctl admin create --username root --email root@example.test --name \"Platform Admin\" --password-stdin",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdin {
				return errors.New("--password-stdin is required")
			}
			pw, err := readPassword(cmd.InOrStdin())
			if err != nil {
				return err
			}
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			u, err := p.CreatePlatformAdmin(context.Background(), username, email, name, pw)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "platform admin created: username=%s id=%s\n", u.Username, u.ID)
			return nil
		},
	}
	create.Flags().StringVar(&username, "username", "", "login name (required)")
	create.Flags().StringVar(&email, "email", "", "email address (required)")
	create.Flags().StringVar(&name, "name", "", "display name (required)")
	create.Flags().BoolVar(&stdin, "password-stdin", false, "read the password from stdin (required)")
	for _, f := range []string{"username", "email", "name"} {
		_ = create.MarkFlagRequired(f)
	}

	cmd.AddCommand(create)
	return cmd
}
