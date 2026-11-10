package endpoint

import (
	"reflect"
	"testing"
)

func TestWanted(t *testing.T) {
	in := []Route{
		{"GET", "/healthz"}, // outside the API
		{"GET", "/api/v1/groups"},
		{"POST", "/api/v1/groups"},
		{"GET", "/api/v1/groups"}, // duplicate
		{"GET", "/api/v1/groups/:id"},
		{"POST", "/api/v1/auth/login"}, // public: never decided by RBAC
		{"GET", "/api/v1/auth/me"},
		{"HEAD", "/api/v1/groups"},    // method not allowed in the table
		{"OPTIONS", "/api/v1/groups"}, // idem
		{"GET", "/api/v10/groups"},    // another prefix that merely starts the same
		{"GET", "/api/v1/authors"},    // starts with "auth" but is not below /auth/
	}
	want := []Route{
		{"GET", "/api/v1/authors"},
		{"GET", "/api/v1/groups"},
		{"POST", "/api/v1/groups"},
		{"GET", "/api/v1/groups/:id"},
	}
	if got := Wanted(in); !reflect.DeepEqual(got, want) {
		t.Errorf("Wanted =\n %v\nwant\n %v", got, want)
	}
}

func TestBuildPlan(t *testing.T) {
	existing := []Endpoint{
		{ID: "1", Method: "GET", Path: "/api/v1/groups", Scope: ScopeTenant},
		{ID: "2", Method: "GET", Path: "/api/v1/removed", Scope: ScopeTenant},
		{ID: "3", Method: "GET", Path: "/api/v1/manual", Scope: ScopePlatform, IsPublic: true}, // edited by hand, no override
	}
	routes := []Route{
		{"GET", "/api/v1/groups"},
		{"POST", "/api/v1/groups"},
		{"GET", "/api/v1/manual"},
	}
	p := BuildPlan(existing, routes)

	if len(p.Insert) != 1 || p.Insert[0].Key() != "POST /api/v1/groups" || p.Insert[0].Scope != ScopeTenant || p.Insert[0].IsPublic {
		t.Errorf("Insert = %+v: a new route must start as tenant scope, not public", p.Insert)
	}
	if len(p.Update) != 0 {
		t.Errorf("Update = %+v: a row without an override must not be changed", p.Update)
	}
	if len(p.Stale) != 1 || p.Stale[0].Key() != "GET /api/v1/removed" {
		t.Errorf("Stale = %+v", p.Stale)
	}
}

func TestBuildPlanAppliesOverrides(t *testing.T) {
	overrides["GET /api/v1/tenants"] = Setting{Scope: ScopePlatform}
	overrides["GET /api/v1/ping"] = Setting{IsPublic: true} // empty scope = tenant
	t.Cleanup(func() {
		delete(overrides, "GET /api/v1/tenants")
		delete(overrides, "GET /api/v1/ping")
	})

	existing := []Endpoint{
		{ID: "1", Method: "GET", Path: "/api/v1/tenants", Scope: ScopeTenant},              // must become platform
		{ID: "2", Method: "GET", Path: "/api/v1/ping", Scope: ScopeTenant, IsPublic: true}, // already right
	}
	routes := []Route{{"GET", "/api/v1/tenants"}, {"GET", "/api/v1/ping"}, {"GET", "/api/v1/new"}}
	p := BuildPlan(existing, routes)

	if len(p.Update) != 1 || p.Update[0].ID != "1" || p.Update[0].Scope != ScopePlatform {
		t.Errorf("Update = %+v", p.Update)
	}
	if len(p.Insert) != 1 || p.Insert[0].Path != "/api/v1/new" {
		t.Errorf("Insert = %+v", p.Insert)
	}
}

func TestBuildPlanInsertsOverriddenRoutesWithTheirSetting(t *testing.T) {
	overrides["GET /api/v1/tenants"] = Setting{Scope: ScopePlatform}
	t.Cleanup(func() { delete(overrides, "GET /api/v1/tenants") })

	p := BuildPlan(nil, []Route{{"GET", "/api/v1/tenants"}})
	if len(p.Insert) != 1 || p.Insert[0].Scope != ScopePlatform {
		t.Errorf("Insert = %+v", p.Insert)
	}
}
