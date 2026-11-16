package user

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
)

// maxReportedErrors is how many problems one import answer lists. The total is always given.
const maxReportedErrors = 100

// RowError is one problem found in the sheet.
type RowError struct {
	Line  int    // row number in the spreadsheet
	Field string // column name
	Rule  string // required, min, max, format, duplicate, taken, unknown
}

// ImportError is returned when the file has problems. Nothing was created.
type ImportError struct {
	Errors []RowError // at most maxReportedErrors
	Total  int        // all problems found
}

func (e *ImportError) Error() string {
	return fmt.Sprintf("user: import refused: %d problems", e.Total)
}

// ImportResult is the outcome of an import. With DryRun the file was only checked.
type ImportResult struct {
	Rows    int  `json:"rows"`
	Created int  `json:"created"`
	DryRun  bool `json:"dry_run"`
}

// Taken finds which usernames and e-mail addresses already belong to a user of the tenant. *Repository satisfies it.
type Taken interface {
	Taken(ctx context.Context, usernames, emails []string) (usernamesTaken, emailsTaken map[string]bool, err error)
}

// GroupLookup returns the id of the group with this name in the tenant of the context, or core.ErrNotFound.
type GroupLookup func(ctx context.Context, name string) (string, error)

// Importer creates many users from a spreadsheet. It is all or nothing: every row is checked first, and users
// are created only when no row has a problem. The request's transaction also rolls everything back when a
// create fails midway.
type Importer struct {
	users  *Service
	taken  Taken
	groups GroupLookup
}

// NewImporter creates the importer. Users are created through the Service, so the password policy and the
// hashing are the same as for a single user.
func NewImporter(users *Service, taken Taken, groups GroupLookup) *Importer {
	return &Importer{users: users, taken: taken, groups: groups}
}

// Import reads an .xlsx file (see ReadSheet) and creates its users in the tenant of the context.
func (im *Importer) Import(ctx context.Context, data []byte, dryRun bool) (*ImportResult, error) {
	rows, err := ReadSheet(data)
	if err != nil {
		return nil, err
	}
	inputs, problems, err := im.check(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, newImportError(problems)
	}
	res := &ImportResult{Rows: len(rows), DryRun: dryRun}
	if dryRun {
		return res, nil
	}
	for i, in := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := im.users.Create(ctx, in); err != nil {
			if errors.Is(err, core.ErrConflict) { // someone created the same user after the check
				return nil, newImportError([]RowError{{Line: rows[i].Line, Field: "username", Rule: "taken"}})
			}
			return nil, err
		}
		res.Created++
	}
	return res, nil
}

func newImportError(all []RowError) *ImportError {
	e := &ImportError{Total: len(all), Errors: all}
	if len(all) > maxReportedErrors {
		e.Errors = all[:maxReportedErrors]
	}
	return e
}

// check validates every row and turns them into inputs. It collects all problems instead of stopping at the first.
func (im *Importer) check(ctx context.Context, rows []Row) ([]CreateInput, []RowError, error) {
	var (
		problems  []RowError
		inputs    = make([]CreateInput, len(rows))
		usernames = map[string]int{}
		emails    = map[string]int{}
		groupIDs  = map[string]*string{}
	)
	bad := func(r Row, field, rule string) { problems = append(problems, RowError{r.Line, field, rule}) }

	for i, r := range rows {
		in := CreateInput{Username: r.Username, Email: r.Email, Name: r.Name, Password: r.Password}

		switch n := utf8.RuneCountInString(r.Username); {
		case n == 0:
			bad(r, "username", "required")
		case n < 3:
			bad(r, "username", "min")
		case n > 64:
			bad(r, "username", "max")
		default:
			if _, dup := usernames[strings.ToLower(r.Username)]; dup {
				bad(r, "username", "duplicate")
			}
			usernames[strings.ToLower(r.Username)] = r.Line
		}

		switch {
		case r.Email == "":
			bad(r, "email", "required")
		case !validEmail(r.Email):
			bad(r, "email", "format")
		default:
			if _, dup := emails[strings.ToLower(r.Email)]; dup {
				bad(r, "email", "duplicate")
			}
			emails[strings.ToLower(r.Email)] = r.Line
		}

		switch n := utf8.RuneCountInString(r.Name); {
		case n == 0:
			bad(r, "name", "required")
		case n > 100:
			bad(r, "name", "max")
		}

		switch {
		case r.Password == "":
			bad(r, "password", "required")
		case errors.Is(password.Validate(r.Password), password.ErrTooShort):
			bad(r, "password", "min")
		case errors.Is(password.Validate(r.Password), password.ErrTooLong):
			bad(r, "password", "max")
		}

		active, ok := parseActive(r.IsActive)
		if !ok {
			bad(r, "is_active", "format")
		}
		in.IsActive = active

		if r.Group != "" {
			id, seen := groupIDs[strings.ToLower(r.Group)]
			if !seen {
				gid, err := im.groups(ctx, r.Group)
				switch {
				case err == nil:
					id = &gid
				case errors.Is(err, core.ErrNotFound):
					id = nil
				default:
					return nil, nil, err
				}
				groupIDs[strings.ToLower(r.Group)] = id
			}
			if id == nil {
				bad(r, "group", "unknown")
			}
			in.GroupID = id
		}
		inputs[i] = in
	}

	// users that already exist in the tenant
	uNames, eAddrs := make([]string, 0, len(usernames)), make([]string, 0, len(emails))
	for u := range usernames {
		uNames = append(uNames, u)
	}
	for e := range emails {
		eAddrs = append(eAddrs, e)
	}
	uTaken, eTaken, err := im.taken.Taken(ctx, uNames, eAddrs)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		if uTaken[strings.ToLower(r.Username)] {
			bad(r, "username", "taken")
		}
		if eTaken[strings.ToLower(r.Email)] {
			bad(r, "email", "taken")
		}
	}
	return inputs, problems, nil
}

// validEmail accepts a plain address like a@b.test, not "Name <a@b.test>".
func validEmail(s string) bool {
	if len(s) > 254 {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && strings.Contains(s[strings.LastIndex(s, "@"):], ".")
}

// parseActive reads the is_active cell. Empty means active.
func parseActive(s string) (*bool, bool) {
	var v bool
	switch strings.ToLower(s) {
	case "":
		return nil, true
	case "yes", "y", "true", "1", "active":
		v = true
	case "no", "n", "false", "0", "inactive":
		v = false
	default:
		return nil, false
	}
	return &v, true
}
