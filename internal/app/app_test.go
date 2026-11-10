package app

import (
	"slices"
	"testing"
)

// The list of routes is what `tenantctl endpoint sync` writes into the endpoints table, so a change here is a
// change of the public surface of the API: it must be deliberate.
func TestRoutes(t *testing.T) {
	var got []string
	for _, r := range Routes(Modules(Deps{})) {
		got = append(got, r.Method+" "+r.Path)
	}
	for _, want := range []string{
		"POST /api/v1/auth/login",
		"GET /api/v1/groups",
		"POST /api/v1/groups",
		"GET /api/v1/groups/:id",
		"PATCH /api/v1/groups/:id",
		"DELETE /api/v1/groups/:id",
		"GET /api/v1/groups/:id/permissions",
		"PUT /api/v1/groups/:id/permissions",
		"GET /api/v1/rbac/endpoints",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing route %q in %v", want, got)
		}
	}
}
