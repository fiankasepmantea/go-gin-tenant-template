package user

import (
	"errors"
	"fmt"
	"strings"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/password"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/xlsx"
)

// Limits of an import file. They keep one request from using the memory or the time of the server.
const (
	// MaxImportBytes is the largest .xlsx file accepted.
	MaxImportBytes = 5 << 20
	// MaxImportRows is the most users one file can create.
	MaxImportRows = 500

	maxScannedLines = 5000 // rows looked at, blank ones included
	unzipLimit      = 50 << 20
	unzipXMLLimit   = 16 << 20
)

// Columns of the import sheet. The first row of the first sheet holds these names, in any order.
var (
	requiredColumns = []string{"username", "email", "name", "password"}
	optionalColumns = []string{"group", "is_active"}
)

var (
	// ErrNotSpreadsheet: the file is not a readable .xlsx workbook.
	ErrNotSpreadsheet = errors.New("user: not an .xlsx file")
	// ErrNoRows: the sheet has a header but no user.
	ErrNoRows = errors.New("user: the sheet has no rows")
	// ErrTooManyRows: more than MaxImportRows users in one file.
	ErrTooManyRows = errors.New("user: too many rows")
)

// MissingColumnsError lists the required columns the header row does not have.
type MissingColumnsError struct{ Columns []string }

func (e *MissingColumnsError) Error() string {
	return "user: missing columns: " + strings.Join(e.Columns, ", ")
}

// Row is one user of the sheet. Line is the row number in the spreadsheet (the header is line 1).
type Row struct {
	Line     int
	Username string
	Email    string
	Name     string
	Password string
	Group    string
	IsActive string
}

// ReadSheet reads the users from the first sheet of an .xlsx file. Cells are read as text, exactly as typed
// (a password of digits stays digits), and rows that are completely empty are skipped.
func ReadSheet(data []byte) ([]Row, error) {
	var (
		index map[string]int
		rows  []Row
	)
	lim := xlsx.Limits{MaxLines: maxScannedLines, MaxColumns: 32, MaxUnzipped: unzipLimit, MaxPart: unzipXMLLimit}
	err := xlsx.ReadFirstSheet(data, lim, func(line int, cells []string) error {
		if index == nil {
			var err error
			index, err = headerIndex(cells)
			return err
		}
		r := Row{
			Line: line, Username: cell(cells, index, "username"), Email: cell(cells, index, "email"),
			Name: cell(cells, index, "name"), Password: cell(cells, index, "password"),
			Group: cell(cells, index, "group"), IsActive: cell(cells, index, "is_active"),
		}
		if r == (Row{Line: line}) {
			return nil // blank row
		}
		if len(rows) == MaxImportRows {
			return ErrTooManyRows
		}
		rows = append(rows, r)
		return nil
	})
	switch {
	case errors.Is(err, xlsx.ErrFormat):
		return nil, fmt.Errorf("%w: %w", ErrNotSpreadsheet, err)
	case errors.Is(err, xlsx.ErrTooManyLines):
		return nil, ErrTooManyRows
	case err != nil:
		return nil, err
	}
	if index == nil {
		return nil, &MissingColumnsError{Columns: requiredColumns}
	}
	if len(rows) == 0 {
		return nil, ErrNoRows
	}
	return rows, nil
}

func headerIndex(cells []string) (map[string]int, error) {
	index := map[string]int{}
	for i, c := range cells {
		name := strings.ToLower(strings.TrimSpace(c))
		if _, dup := index[name]; name != "" && !dup {
			index[name] = i
		}
	}
	var missing []string
	for _, c := range requiredColumns {
		if _, ok := index[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) > 0 {
		return nil, &MissingColumnsError{Columns: missing}
	}
	return index, nil
}

// cell returns the trimmed text of a column, or "" when the row is shorter or the column is absent. The
// password is the one value that is not trimmed in the sheet, because spaces can be part of it.
func cell(cells []string, index map[string]int, column string) string {
	i, ok := index[column]
	if !ok || i >= len(cells) {
		return ""
	}
	if column == "password" {
		return cells[i]
	}
	return strings.TrimSpace(cells[i])
}

// Template returns an .xlsx file with the header row of the import sheet and a help sheet.
func Template() ([]byte, error) {
	head := append(append([]string{}, requiredColumns...), optionalColumns...)
	return xlsx.Write(
		xlsx.Sheet{Name: "Users", Header: true, Widths: []float64{22, 22, 22, 22, 22, 22}, Rows: [][]string{head}},
		xlsx.Sheet{Name: "Help", Widths: []float64{100}, Rows: [][]string{
			{"Fill the Users sheet, one user per row, then upload the file. Only the first sheet is read."},
			{"username: 3 to 64 characters. email: a valid address. name: up to 100 characters."},
			{fmt.Sprintf("password: at least %d characters. It is stored as a hash and cannot be shown again.", password.MinLength)},
			{"group (optional): the NAME of an existing group. Leave empty for no group."},
			{"is_active (optional): yes or no. Empty means yes."},
			{fmt.Sprintf("At most %d users per file. If one row is wrong, nothing is imported.", MaxImportRows)},
		}},
	)
}
