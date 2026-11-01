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
		return nil, nil, err
	}
	return NewProvisioner(db, hasher), func() { closeDB(db) }, nil
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

	var code, name string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a tenant",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, closeFn, err := openProvisioner()
			if err != nil {
				return err
			}
			defer closeFn()
			t, err := p.CreateTenant(context.Background(), code, name)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "tenant created: code=%s id=%s\n", t.Code, t.ID)
			return nil
		},
	}
	create.Flags().StringVar(&code, "code", "", "tenant code used at login, e.g. acme (required)")
	create.Flags().StringVar(&name, "name", "", "display name (required)")
	_ = create.MarkFlagRequired("code")
	_ = create.MarkFlagRequired("name")

	cmd.AddCommand(create)
	return cmd
}

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "Manage tenant users"}

	var tenantCode, username, email, name string
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
			return nil
		},
	}
	create.Flags().StringVar(&tenantCode, "tenant", "", "tenant code (required)")
	create.Flags().StringVar(&username, "username", "", "login name (required)")
	create.Flags().StringVar(&email, "email", "", "email address (required)")
	create.Flags().StringVar(&name, "name", "", "display name (required)")
	create.Flags().BoolVar(&stdin, "password-stdin", false, "read the password from stdin (required)")
	for _, f := range []string{"tenant", "username", "email", "name"} {
		_ = create.MarkFlagRequired(f)
	}

	cmd.AddCommand(create)
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
