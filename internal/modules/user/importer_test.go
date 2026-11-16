package user

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/xlsx"
)

// workbook builds an .xlsx file: the first slice is the header row, the others are data rows.
func workbook(t *testing.T, header []string, rows ...[]string) []byte {
	t.Helper()
	data, err := xlsx.Write(xlsx.Sheet{Name: "Sheet1", Rows: append([][]string{header}, rows...)})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var head = []string{"username", "email", "name", "password", "group", "is_active"}

const goodPW = "a-long-enough-password"

type fakeTaken struct{ users, emails map[string]bool }

func (f fakeTaken) Taken(context.Context, []string, []string) (map[string]bool, map[string]bool, error) {
	return f.users, f.emails, nil
}

// recordingStore counts the users that are created.
type recordingStore struct {
	fakeStore
	made   []string
	failAt int
}

func (r *recordingStore) Create(_ context.Context, e *User) error {
	if r.failAt > 0 && len(r.made)+1 == r.failAt {
		return core.ErrConflict
	}
	r.made = append(r.made, e.Username)
	return nil
}

func newImporter(taken fakeTaken) (*Importer, *recordingStore) {
	st := &recordingStore{}
	svc := NewService(st, &fakeSessions{}, fakeHasher{})
	groups := func(_ context.Context, name string) (string, error) {
		if strings.EqualFold(name, "staff") {
			return "group-staff", nil
		}
		return "", core.ErrNotFound
	}
	return NewImporter(svc, taken, groups), st
}

func problems(t *testing.T, err error) []RowError {
	t.Helper()
	var ie *ImportError
	if !errors.As(err, &ie) {
		t.Fatalf("want ImportError, got %v", err)
	}
	return ie.Errors
}

func TestImport_CreatesAll(t *testing.T) {
	im, st := newImporter(fakeTaken{})
	data := workbook(t, head,
		[]string{"budi", "budi@a.test", "Budi", goodPW, "Staff", ""},
		[]string{"siti", "siti@a.test", "Siti", goodPW, "", "no"},
		[]string{},
		[]string{"1001", "n@a.test", "Numeric", "12345678901234", "", "yes"},
	)
	res, err := im.Import(context.Background(), data, false)
	if err != nil || res.Rows != 3 || res.Created != 3 || res.DryRun {
		t.Fatalf("import: %+v %v", res, err)
	}
	if strings.Join(st.made, ",") != "budi,siti,1001" {
		t.Fatalf("created: %v", st.made)
	}
}

func TestImport_DryRunCreatesNothing(t *testing.T) {
	im, st := newImporter(fakeTaken{})
	res, err := im.Import(context.Background(), workbook(t, head, []string{"budi", "b@a.test", "Budi", goodPW}), true)
	if err != nil || !res.DryRun || res.Created != 0 || res.Rows != 1 || len(st.made) != 0 {
		t.Fatalf("dry run: %+v %v %v", res, err, st.made)
	}
}

func TestImport_ReportsEveryProblemAndCreatesNothing(t *testing.T) {
	im, st := newImporter(fakeTaken{users: map[string]bool{"taken": true}, emails: map[string]bool{"old@a.test": true}})
	data := workbook(t, head,
		[]string{"ab", "x@a.test", "Short", goodPW},                         // line 2: username min
		[]string{"okay", "not-an-email", "Mail", goodPW},                    // 3: email format
		[]string{"pwless", "p@a.test", "", "short"},                         // 4: name required, password min
		[]string{"okay2", "o2@a.test", "Grp", goodPW, "nogroup"},            // 5: group unknown
		[]string{"flag", "f@a.test", "Flag", goodPW, "", "maybe"},           // 6: is_active format
		[]string{"dup", "d@a.test", "Dup", goodPW},                          // 7 fine
		[]string{"DUP", "d@a.test", "Dup", goodPW},                          // 8: username + email duplicate
		[]string{"taken", "old@a.test", "Taken", goodPW},                    // 9: both taken
		[]string{"x" + strings.Repeat("y", 70), "l@a.test", "Long", goodPW}, // 10: username max
	)
	_, err := im.Import(context.Background(), data, false)
	got := map[string]bool{}
	for _, p := range problems(t, err) {
		got[strconv.Itoa(p.Line)+" "+p.Field+" "+p.Rule] = true
	}
	for _, want := range []string{
		"2 username min", "3 email format", "4 name required", "4 password min", "5 group unknown", "6 is_active format",
		"8 username duplicate", "8 email duplicate", "9 username taken", "9 email taken", "10 username max",
	} {
		if !got[want] {
			t.Errorf("missing problem %q in %v", want, got)
		}
	}
	if got["7 username duplicate"] {
		t.Error("the first of two equal rows is not the duplicate")
	}
	if len(st.made) != 0 {
		t.Fatalf("created %v although the file has problems", st.made)
	}
}

func TestImport_ConflictWhileCreating(t *testing.T) {
	im, st := newImporter(fakeTaken{})
	st.failAt = 2
	data := workbook(t, head,
		[]string{"a-user", "a@a.test", "A", goodPW}, []string{"b-user", "b@a.test", "B", goodPW})
	_, err := im.Import(context.Background(), data, false)
	p := problems(t, err)
	if len(p) != 1 || p[0].Line != 3 || p[0].Rule != "taken" {
		t.Fatalf("conflict: %+v", p)
	}
}

func TestImport_TooManyProblemsAreCapped(t *testing.T) {
	im, _ := newImporter(fakeTaken{})
	var rows [][]string
	for i := 0; i < 150; i++ {
		rows = append(rows, []string{"user" + strconv.Itoa(i), "bad", "N", goodPW})
	}
	_, err := im.Import(context.Background(), workbook(t, head, rows...), false)
	var ie *ImportError
	if !errors.As(err, &ie) || ie.Total != 150 || len(ie.Errors) != maxReportedErrors {
		t.Fatalf("cap: %+v", ie)
	}
}

func TestReadSheet(t *testing.T) {
	// columns in any order, header case and spaces ignored, extra columns ignored
	data := workbook(t, []string{" Password ", "NAME", "extra", "Email", "Username"},
		[]string{goodPW, "Budi", "x", "b@a.test", " budi "})
	rows, err := ReadSheet(data)
	if err != nil || len(rows) != 1 || rows[0].Username != "budi" || rows[0].Email != "b@a.test" || rows[0].Password != goodPW {
		t.Fatalf("reorder: %+v %v", rows, err)
	}

	var mc *MissingColumnsError
	if _, err := ReadSheet(workbook(t, []string{"username", "email"}, []string{"a", "b"})); !errors.As(err, &mc) ||
		strings.Join(mc.Columns, ",") != "name,password" {
		t.Fatalf("missing columns: %v", err)
	}
	if _, err := ReadSheet(workbook(t, head)); !errors.Is(err, ErrNoRows) {
		t.Fatalf("no rows: %v", err)
	}
	for _, junk := range [][]byte{nil, []byte("username,email\na,b"), []byte("PK\x03\x04 not really")} {
		if _, err := ReadSheet(junk); !errors.Is(err, ErrNotSpreadsheet) {
			t.Errorf("junk %q: %v", junk, err)
		}
	}

	var many [][]string
	for i := 0; i <= MaxImportRows; i++ {
		many = append(many, []string{"u" + strconv.Itoa(i), "e", "n", "p"})
	}
	if _, err := ReadSheet(workbook(t, head, many...)); !errors.Is(err, ErrTooManyRows) {
		t.Fatalf("too many: %v", err)
	}
	if _, err := ReadSheet(workbook(t, head, many[:MaxImportRows]...)); err != nil {
		t.Fatalf("exactly the maximum: %v", err)
	}
}

func TestTemplate_RoundTrips(t *testing.T) {
	data, err := Template()
	if err != nil {
		t.Fatal(err)
	}
	// the template is a valid workbook whose header is accepted; it has no users yet
	if _, err := ReadSheet(data); !errors.Is(err, ErrNoRows) {
		t.Fatalf("template: %v", err)
	}
	var first []string
	err = xlsx.ReadFirstSheet(data, xlsx.Limits{}, func(line int, cells []string) error {
		if line == 1 {
			first = cells
		}
		return nil
	})
	if err != nil || strings.Join(first, ",") != "username,email,name,password,group,is_active" {
		t.Fatalf("header row: %v %v", first, err)
	}
}
