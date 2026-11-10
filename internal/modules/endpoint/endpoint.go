// Package endpoint is the whitelist of API routes: every route that needs authorization has one row here, and
// a request to a route without a row is refused (default deny). The rows are created from the router by
// `tenantctl endpoint sync`, never typed by hand, so the table cannot drift from the code.
//
// An endpoint is identified by the HTTP method and the Gin route PATTERN (for example
// "GET /api/v1/groups/:id"), not by the URL of a request, so IDs and other parameters cannot be used to
// slip past a rule.
package endpoint

import (
	"slices"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Scope says which level of token may use an endpoint.
const (
	// ScopeTenant: users of a tenant. Needs a group grant unless the endpoint is public.
	ScopeTenant = "tenant"
	// ScopePlatform: platform admins only, always allowed for them (no grants).
	ScopePlatform = "platform"
)

// APIPrefix is where the versioned API lives; only routes below it are synchronized.
const APIPrefix = "/api/v1/"

// skipPrefixes are routes that RBAC never decides: they are public or run their own authentication.
var skipPrefixes = []string{APIPrefix + "auth/"}

// overrides are the endpoints whose scope or is_public differs from the default (tenant, not public).
// Add an entry here when a route must be public to every authenticated user, or reserved for platform admins:
//
//	"GET /api/v1/tenants": {Scope: ScopePlatform},
var overrides = map[string]Setting{}

// Setting is the access rule of one endpoint, apart from the group grants.
type Setting struct {
	Scope    string
	IsPublic bool
}

// Endpoint is one row of the endpoints table (global: no tenant).
type Endpoint struct {
	ID        string         `gorm:"column:endpoint_id;primaryKey" json:"endpoint_id"`
	Unique    int64          `gorm:"column:endpoint_unique;->" json:"-"`
	Method    string         `gorm:"column:method" json:"method"`
	Path      string         `gorm:"column:path" json:"path"`
	Name      string         `gorm:"column:name" json:"name"`
	Scope     string         `gorm:"column:scope" json:"scope"`
	IsPublic  bool           `gorm:"column:is_public" json:"is_public"`
	CreatedAt time.Time      `gorm:"column:created_at" json:"created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"column:deleted_at;index" json:"-"`
}

func (*Endpoint) TableName() string  { return "endpoints" }
func (*Endpoint) PrimaryKey() string { return "endpoint_id" }
func (e *Endpoint) GetID() string    { return e.ID }
func (e *Endpoint) SetID(id string)  { e.ID = id }

// Key is "METHOD pattern", the identity of an endpoint.
func (e *Endpoint) Key() string { return Key(e.Method, e.Path) }

// Key builds the identity of an endpoint.
func Key(method, path string) string { return method + " " + path }

// Route is a method and a Gin route pattern, as reported by the router.
type Route struct {
	Method string
	Path   string
}

var allowedMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

// Wanted keeps the routes that need an endpoint row: below APIPrefix, not skipped, with a method the table
// accepts. The result is sorted and has no duplicates.
func Wanted(routes []Route) []Route {
	seen := make(map[string]bool, len(routes))
	var out []Route
	for _, r := range routes {
		if !strings.HasPrefix(r.Path, APIPrefix) || !slices.Contains(allowedMethods, r.Method) {
			continue
		}
		if slices.ContainsFunc(skipPrefixes, func(p string) bool { return strings.HasPrefix(r.Path, p) }) {
			continue
		}
		if k := Key(r.Method, r.Path); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// settingFor returns the access rule of a route: the override, or the default (tenant scope, not public).
func settingFor(r Route) (s Setting, overridden bool) {
	if o, ok := overrides[Key(r.Method, r.Path)]; ok {
		if o.Scope == "" {
			o.Scope = ScopeTenant
		}
		return o, true
	}
	return Setting{Scope: ScopeTenant}, false
}

// Plan is what a synchronization has to do.
type Plan struct {
	Insert []Endpoint // routes without a row
	Update []Endpoint // rows whose override changed (the new values are in the entries)
	Stale  []Endpoint // rows whose route no longer exists
}

// BuildPlan compares the stored endpoints with the routes of the router. Existing rows are only changed when
// the code declares an override for them; a row without an override keeps whatever it has.
func BuildPlan(existing []Endpoint, routes []Route) Plan {
	var p Plan
	have := make(map[string]Endpoint, len(existing))
	for _, e := range existing {
		have[e.Key()] = e
	}
	want := make(map[string]bool, len(routes))
	for _, r := range routes {
		k := Key(r.Method, r.Path)
		want[k] = true
		s, overridden := settingFor(r)
		cur, ok := have[k]
		switch {
		case !ok:
			p.Insert = append(p.Insert, Endpoint{Method: r.Method, Path: r.Path, Name: k, Scope: s.Scope, IsPublic: s.IsPublic})
		case overridden && (cur.Scope != s.Scope || cur.IsPublic != s.IsPublic):
			cur.Scope, cur.IsPublic = s.Scope, s.IsPublic
			p.Update = append(p.Update, cur)
		}
	}
	for _, e := range existing {
		if !want[e.Key()] {
			p.Stale = append(p.Stale, e)
		}
	}
	sort.Slice(p.Stale, func(i, j int) bool { return p.Stale[i].Key() < p.Stale[j].Key() })
	return p
}
