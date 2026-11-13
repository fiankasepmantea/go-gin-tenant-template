//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// userAdmin logs in alice of tokoa as a member of a group that may use every endpoint.
func userAdmin(t *testing.T) (*rbacEnv, string) {
	t.Helper()
	e := setupRBAC(t)
	ctx := context.Background()
	e.sync(t)
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	return e, login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")
}

func userID(t *testing.T, res apiResp) string {
	t.Helper()
	var u struct {
		ID string `json:"user_id"`
	}
	if err := json.Unmarshal(res.Body.Data, &u); err != nil || u.ID == "" {
		t.Fatalf("no user in response: %s", res.Raw)
	}
	return u.ID
}

func TestUsersAPI_Lifecycle(t *testing.T) {
	e, tok := userAdmin(t)
	const base = "/api/v1/users"

	res := call(t, e.r, http.MethodPost, base, tok,
		`{"username":"budi","email":"budi@a.test","name":"Budi","password":"a-long-enough-password"}`)
	if res.Status != 201 || strings.Contains(res.Raw, "password") {
		t.Fatalf("create: %d %s", res.Status, res.Raw)
	}
	id := userID(t, res)

	// the new user can log in, and only inside the own tenant
	budi := login(t, e.r, "/api/v1/auth/login", "tokoa", "budi", "a-long-enough-password")
	if res := call(t, e.r, http.MethodPost, "/api/v1/auth/login", "",
		`{"username":"budi","password":"a-long-enough-password"}`, "X-Tenant-Code", "tokob"); res.Status != 401 {
		t.Fatalf("login in the other tenant: %d", res.Status)
	}

	// duplicates (case-insensitive) are 409, a weak password is 422
	if res := call(t, e.r, http.MethodPost, base, tok,
		`{"username":"BUDI","email":"x@a.test","name":"B","password":"a-long-enough-password"}`); res.Status != 409 {
		t.Fatalf("duplicate: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodPost, base, tok,
		`{"username":"siti","email":"siti@a.test","name":"Siti","password":"short"}`); res.Status != 422 {
		t.Fatalf("weak password: %d %s", res.Status, res.Raw)
	}
	// the same username is fine in the other tenant: alice exists in both
	if res := call(t, e.r, http.MethodGet, base+"?username=alice", tok, ""); res.Status != 200 ||
		strings.Count(res.Raw, `"alice"`) != 1 {
		t.Fatalf("list filter: %d %s", res.Status, res.Raw)
	}

	// a group of another tenant (or a random id) is refused
	if res := call(t, e.r, http.MethodPatch, base+"/"+id, tok,
		`{"group_id":"00000000-0000-7000-8000-000000000000"}`); res.Status != 422 {
		t.Fatalf("foreign group: %d %s", res.Status, res.Raw)
	}

	// admin sets a new password: old one stops working, sessions end
	if res := call(t, e.r, http.MethodPost, base+"/"+id+"/password", tok, `{"password":"another-long-password"}`); res.Status != 200 {
		t.Fatalf("set password: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodPost, "/api/v1/auth/login", "",
		`{"username":"budi","password":"a-long-enough-password"}`, "X-Tenant-Code", "tokoa"); res.Status != 401 {
		t.Fatalf("old password: %d", res.Status)
	}
	_ = budi
	budi = login(t, e.r, "/api/v1/auth/login", "tokoa", "budi", "another-long-password")

	// patch: name changes, is_active=false locks the user out
	if res := call(t, e.r, http.MethodPatch, base+"/"+id, tok, `{"name":"Budi S"}`); res.Status != 200 || !strings.Contains(res.Raw, "Budi S") {
		t.Fatalf("patch: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodPatch, base+"/"+id, tok, `{}`); res.Status != 400 {
		t.Fatalf("empty patch: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPatch, base+"/"+id, tok, `{"is_active":false}`); res.Status != 200 {
		t.Fatalf("deactivate: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodGet, base, budi, ""); res.Status != 401 {
		t.Fatalf("token of a deactivated user must stop working at refresh/login; access token status %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPost, "/api/v1/auth/login", "",
		`{"username":"budi","password":"another-long-password"}`, "X-Tenant-Code", "tokoa"); res.Status != 401 {
		t.Fatalf("login of a deactivated user: %d", res.Status)
	}

	// delete
	if res := call(t, e.r, http.MethodDelete, base+"/"+id, tok, ""); res.Status != 200 {
		t.Fatalf("delete: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodGet, base+"/"+id, tok, ""); res.Status != 404 {
		t.Fatalf("get after delete: %d", res.Status)
	}
}

func TestUsersAPI_TenantIsolationAndGuards(t *testing.T) {
	e, tok := userAdmin(t)
	const base = "/api/v1/users"

	// the user list of tokoa never shows tokob; alice of tokob cannot be reached by id either
	res := call(t, e.r, http.MethodGet, base, tok, "")
	if res.Status != 200 || strings.Contains(res.Raw, "alice@b.test") || strings.Contains(res.Raw, "root@x.test") {
		t.Fatalf("list leaks: %d %s", res.Status, res.Raw)
	}
	var alice string
	for _, u := range page2(t, res) {
		if u.Username == "alice" {
			alice = u.ID
		}
	}
	if alice == "" {
		t.Fatal("alice not listed")
	}
	// own account: neither deactivation nor deletion
	if res := call(t, e.r, http.MethodPatch, base+"/"+alice, tok, `{"is_active":false}`); res.Status != 403 {
		t.Fatalf("self deactivate: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodDelete, base+"/"+alice, tok, ""); res.Status != 403 {
		t.Fatalf("self delete: %d", res.Status)
	}
	// not a uuid, unknown id
	if res := call(t, e.r, http.MethodGet, base+"/nope", tok, ""); res.Status != 404 {
		t.Fatalf("bad id: %d", res.Status)
	}
	// a member of a group without grants may not use the API
	if _, err := e.prov.CreateTenantUser(context.Background(), "tokoa", "carl", "carl@a.test", "Carl", "password-of-carl-aa"); err != nil {
		t.Fatal(err)
	}
	carl := login(t, e.r, "/api/v1/auth/login", "tokoa", "carl", "password-of-carl-aa")
	if res := call(t, e.r, http.MethodGet, base, carl, ""); res.Status != 403 {
		t.Fatalf("no grants: %d", res.Status)
	}
	// unauthenticated
	if res := call(t, e.r, http.MethodGet, base, "", ""); res.Status != 401 {
		t.Fatalf("anonymous: %d", res.Status)
	}
}

type listedUser struct {
	ID       string `json:"user_id"`
	Username string `json:"username"`
}

func page2(t *testing.T, res apiResp) []listedUser {
	t.Helper()
	var p struct {
		Data []listedUser `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Data, &p); err != nil {
		t.Fatalf("%v: %s", err, res.Raw)
	}
	return p.Data
}

func mustJSON(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
}
