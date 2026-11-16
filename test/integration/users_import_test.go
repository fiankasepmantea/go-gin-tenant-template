//go:build integration

package integration

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

const (
	importPath   = "/api/v1/users/import"
	importHeader = "username,email,name,password,group,is_active"
	importPW     = "a-long-enough-password"
)

// sheet builds an .xlsx file from rows of comma separated cells (no commas inside cells).
func sheet(t *testing.T, lines ...string) []byte {
	t.Helper()
	f := excelize.NewFile()
	for r, line := range lines {
		for c, v := range strings.Split(line, ",") {
			name, _ := excelize.CoordinatesToCellName(c+1, r+1)
			if err := f.SetCellValue("Sheet1", name, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func (e *rbacEnv) importUsers(t *testing.T, tok, query string, content []byte) apiResp {
	t.Helper()
	body, ct := form(t, "users.xlsx", content)
	return call(t, e.r, http.MethodPost, importPath+query, tok, body, "Content-Type", ct)
}

func TestUsersImport(t *testing.T) {
	e, tok := userAdmin(t)
	if res := call(t, e.r, http.MethodPost, "/api/v1/groups", tok, `{"name":"Staff"}`); res.Status != 201 {
		t.Fatalf("group: %d %s", res.Status, res.Raw)
	}
	count := func(name string) int {
		res := call(t, e.r, http.MethodGet, "/api/v1/users?username="+name, tok, "")
		return len(page2(t, res))
	}

	good := sheet(t, importHeader,
		"budi,budi@import.test,Budi,"+importPW+",staff,",
		"siti,siti@import.test,Siti,"+importPW+",,no",
	)

	// dry run: checked, not created
	res := e.importUsers(t, tok, "?dry_run=true", good)
	if res.Status != 200 || !strings.Contains(res.Raw, `"dry_run":true`) || !strings.Contains(res.Raw, `"created":0`) || count("budi") != 0 {
		t.Fatalf("dry run: %d %s", res.Status, res.Raw)
	}

	// the real import: created, in the group, inactive when asked, and able to log in
	res = e.importUsers(t, tok, "", good)
	if res.Status != 200 || !strings.Contains(res.Raw, `"created":2`) || strings.Contains(res.Raw, importPW) {
		t.Fatalf("import: %d %s", res.Status, res.Raw)
	}
	login(t, e.r, "/api/v1/auth/login", "tokoa", "budi", importPW)
	if got := call(t, e.r, http.MethodGet, "/api/v1/users?username=siti", tok, ""); !strings.Contains(got.Raw, `"is_active":false`) {
		t.Fatalf("siti must be inactive: %s", got.Raw)
	}
	if n := e.count(t, "SELECT count(*) FROM users u JOIN groups g ON g.group_id = u.group_id WHERE u.username = 'budi' AND g.name = 'Staff'"); n != 1 {
		t.Fatal("budi is not in the group Staff")
	}

	// importing the same file again: both are taken, nothing changes
	res = e.importUsers(t, tok, "", good)
	if res.Status != 422 || !strings.Contains(res.Raw, "row 2 username") || !strings.Contains(res.Raw, `"taken"`) {
		t.Fatalf("again: %d %s", res.Status, res.Raw)
	}

	// one bad row: the good rows before it are not created either
	mixed := sheet(t, importHeader,
		"andi,andi@import.test,Andi,"+importPW+",,",
		"rina,rina@import.test,Rina,short,,",
		"dewi,dewi@import.test,Dewi,"+importPW+",nogroup,",
	)
	res = e.importUsers(t, tok, "", mixed)
	if res.Status != 422 || !strings.Contains(res.Raw, "row 3 password") || !strings.Contains(res.Raw, "row 4 group") || count("andi") != 0 {
		t.Fatalf("mixed: %d %s", res.Status, res.Raw)
	}

	// the same usernames are free in the other tenant: the check is per tenant
	ctx := context.Background()
	if _, err := e.prov.CreateGroup(ctx, "tokob", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokob", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokob", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	other := login(t, e.r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")
	// ... and the group Staff of tokoa does not exist there
	if res := e.importUsers(t, other, "", good); res.Status != 422 || !strings.Contains(res.Raw, "row 2 group") {
		t.Fatalf("other tenant, group of another tenant: %d %s", res.Status, res.Raw)
	}
	nogroup := sheet(t, importHeader, "budi,budi@import.test,Budi,"+importPW+",,")
	if res := e.importUsers(t, other, "", nogroup); res.Status != 200 {
		t.Fatalf("other tenant: %d %s", res.Status, res.Raw)
	}
}

func TestUsersImport_BadFiles(t *testing.T) {
	e, tok := userAdmin(t)
	for name, c := range map[string]struct {
		content []byte
		status  int
		want    string
	}{
		"not a workbook":  {[]byte("username,email\nx,y"), 422, `"file"`},
		"missing columns": {sheet(t, "username,email", "a,b"), 422, "missing"},
		"no users":        {sheet(t, importHeader), 422, "empty"},
	} {
		if res := e.importUsers(t, tok, "", c.content); res.Status != c.status || !strings.Contains(res.Raw, c.want) {
			t.Errorf("%s: %d %s", name, res.Status, res.Raw)
		}
	}
	if res := e.importUsers(t, tok, "?dry_run=maybe", sheet(t, importHeader, "a")); res.Status != 422 {
		t.Errorf("bad dry_run: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, importPath, tok, `{"a":1}`); res.Status != 400 {
		t.Errorf("json instead of multipart: %d", res.Status)
	}
	body, ct := form(t, "", nil)
	if res := call(t, e.r, http.MethodPost, importPath, tok, body, "Content-Type", ct); res.Status != 422 {
		t.Errorf("no file field: %d", res.Status)
	}
	big := bytes.Repeat([]byte("x"), 6<<20)
	if res := e.importUsers(t, tok, "", big); res.Status != 413 {
		t.Errorf("too large: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, importPath, "", ""); res.Status != 401 {
		t.Errorf("anonymous: %d", res.Status)
	}
}

func TestUsersImport_TemplateAndAccess(t *testing.T) {
	e, tok := userAdmin(t)

	w := e.raw(t, importPath+"/template", tok)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "users-template.xlsx") ||
		w.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
		t.Fatalf("template: %d %v", w.Code, w.Header())
	}
	f, err := excelize.OpenReader(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.GetCellValue("Users", "A1"); v != "username" {
		t.Fatalf("template header: %q", v)
	}

	// a member without a grant cannot import: it is a normal tenant endpoint
	ctx := context.Background()
	if res := call(t, e.r, http.MethodPost, "/api/v1/users", tok,
		`{"username":"bob","email":"bob@a.test","name":"Bob","password":"`+importPW+`"}`); res.Status != 201 {
		t.Fatalf("bob: %d %s", res.Status, res.Raw)
	}
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "viewer", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "bob", "viewer"); err != nil {
		t.Fatal(err)
	}
	settle()
	bob := login(t, e.r, "/api/v1/auth/login", "tokoa", "bob", importPW)
	if res := e.importUsers(t, bob, "", sheet(t, importHeader, "a")); res.Status != 403 {
		t.Errorf("no grant: %d", res.Status)
	}
	if w := e.raw(t, importPath+"/template", bob); w.Code != 403 {
		t.Errorf("template without grant: %d", w.Code)
	}
	if n := e.count(t, "SELECT count(*) FROM endpoints WHERE path IN ('/api/v1/users/import','/api/v1/users/import/template') AND scope='tenant'"); n != 2 {
		t.Errorf("import endpoints = %d, want 2", n)
	}
}
