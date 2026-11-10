package rbac

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
)

const (
	tenantA = "0198f6a0-0000-7000-8000-00000000000a"
	tenantB = "0198f6a0-0000-7000-8000-00000000000b"
	userOK  = "0198f6a0-0000-7000-8000-0000000000a1"
	group1  = "0198f6a0-0000-7000-8000-0000000000c1"
)

func str(s string) *string { return &s }

type fakeEndpoints struct {
	list  []endpoint.Endpoint
	err   error
	calls atomic.Int32
}

func (f *fakeEndpoints) All(context.Context) ([]endpoint.Endpoint, error) {
	f.calls.Add(1)
	return f.list, f.err
}

type fakeAccess struct {
	users       map[string]UserAccess // by user id
	grants      map[string][]string   // by group id
	userErr     error
	grantErr    error
	userCalls   atomic.Int32
	grantsCalls atomic.Int32
}

func (f *fakeAccess) UserAccess(_ context.Context, id string) (UserAccess, error) {
	f.userCalls.Add(1)
	if f.userErr != nil {
		return UserAccess{}, f.userErr
	}
	a, ok := f.users[id]
	if !ok {
		return UserAccess{}, core.ErrNotFound
	}
	return a, nil
}

func (f *fakeAccess) GroupEndpointIDs(_ context.Context, id string) ([]string, error) {
	f.grantsCalls.Add(1)
	return f.grants[id], f.grantErr
}

var catalog = []endpoint.Endpoint{
	{ID: "e-list", Method: "GET", Path: "/api/v1/things", Scope: endpoint.ScopeTenant},
	{ID: "e-get", Method: "GET", Path: "/api/v1/things/:id", Scope: endpoint.ScopeTenant},
	{ID: "e-del", Method: "DELETE", Path: "/api/v1/things/:id", Scope: endpoint.ScopeTenant},
	{ID: "e-public", Method: "GET", Path: "/api/v1/profile", Scope: endpoint.ScopeTenant, IsPublic: true},
	{ID: "e-tenants", Method: "GET", Path: "/api/v1/tenants", Scope: endpoint.ScopePlatform},
}

func newFixture() (*Service, *fakeEndpoints, *fakeAccess) {
	eps := &fakeEndpoints{list: catalog}
	acc := &fakeAccess{
		users: map[string]UserAccess{
			userOK:   {GroupID: str(group1), Active: true},
			"nogrp":  {GroupID: nil, Active: true},
			"off":    {GroupID: str(group1), Active: false},
			"intact": {GroupID: str("empty-group"), Active: true},
		},
		grants: map[string][]string{group1: {"e-list", "e-get"}},
	}
	return NewService(eps, acc, Options{}), eps, acc
}

func tenantClaims(user string) *auth.Claims {
	return &auth.Claims{UserID: user, TenantID: tenantA, Level: auth.LevelTenant}
}

func TestAuthorizeDecisions(t *testing.T) {
	platform := &auth.Claims{UserID: "root", Level: auth.LevelPlatform}
	tests := []struct {
		name        string
		claims      *auth.Claims
		method      string
		route       string
		wantVerdict auth.Verdict
		wantReason  string
	}{
		{"granted", tenantClaims(userOK), "GET", "/api/v1/things", auth.VerdictAllowed, ReasonAllowed},
		{"granted by pattern", tenantClaims(userOK), "GET", "/api/v1/things/:id", auth.VerdictAllowed, ReasonAllowed},
		{"same path, method not granted", tenantClaims(userOK), "DELETE", "/api/v1/things/:id", auth.VerdictForbidden, ReasonNotGranted},
		{"public endpoint needs no grant", tenantClaims("nogrp"), "GET", "/api/v1/profile", auth.VerdictAllowed, ReasonPublic},
		{"no group", tenantClaims("nogrp"), "GET", "/api/v1/things", auth.VerdictForbidden, ReasonNoGroup},
		{"group without grants", tenantClaims("intact"), "GET", "/api/v1/things", auth.VerdictForbidden, ReasonNotGranted},
		{"unregistered route is refused", tenantClaims(userOK), "GET", "/api/v1/secret", auth.VerdictForbidden, ReasonUnregistered},
		{"the raw URL is not a pattern", tenantClaims(userOK), "GET", "/api/v1/things/123", auth.VerdictForbidden, ReasonUnregistered},
		{"inactive user", tenantClaims("off"), "GET", "/api/v1/things", auth.VerdictUnauthenticated, ReasonUserInactive},
		{"inactive user, even on a public endpoint", tenantClaims("off"), "GET", "/api/v1/profile", auth.VerdictUnauthenticated, ReasonUserInactive},
		{"user no longer exists", tenantClaims("ghost"), "GET", "/api/v1/things", auth.VerdictUnauthenticated, ReasonUserGone},
		{"tenant user on a platform endpoint", tenantClaims(userOK), "GET", "/api/v1/tenants", auth.VerdictForbidden, ReasonWrongLevel},
		{"platform admin on a platform endpoint", platform, "GET", "/api/v1/tenants", auth.VerdictAllowed, ReasonPlatform},
		{"platform admin on a tenant endpoint", platform, "GET", "/api/v1/things", auth.VerdictForbidden, ReasonWrongLevel},
		{"platform admin on a public tenant endpoint", platform, "GET", "/api/v1/profile", auth.VerdictForbidden, ReasonWrongLevel},
		{"platform admin on an unknown route", platform, "GET", "/api/v1/secret", auth.VerdictForbidden, ReasonUnregistered},
		{"unknown level", &auth.Claims{UserID: userOK, Level: "root"}, "GET", "/api/v1/things", auth.VerdictForbidden, ReasonWrongLevel},
		{"no claims", nil, "GET", "/api/v1/things", auth.VerdictUnauthenticated, ReasonNoClaims},
		{"method is case sensitive", tenantClaims(userOK), "get", "/api/v1/things", auth.VerdictForbidden, ReasonUnregistered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := newFixture()
			d, err := s.Authorize(context.Background(), tt.claims, tt.method, tt.route)
			if err != nil {
				t.Fatal(err)
			}
			if d.Verdict != tt.wantVerdict || d.Reason != tt.wantReason {
				t.Errorf("got %+v, want verdict %v reason %s", d, tt.wantVerdict, tt.wantReason)
			}
		})
	}
}

func TestAuthorizeFailsClosedOnErrors(t *testing.T) {
	boom := errors.New("database is down")
	t.Run("endpoint list", func(t *testing.T) {
		s, eps, _ := newFixture()
		eps.err = boom
		d, err := s.Authorize(context.Background(), tenantClaims(userOK), "GET", "/api/v1/things")
		if !errors.Is(err, boom) || d.Verdict == auth.VerdictAllowed {
			t.Errorf("got %+v, %v: an error must never allow", d, err)
		}
	})
	t.Run("user lookup", func(t *testing.T) {
		s, _, acc := newFixture()
		acc.userErr = boom
		d, err := s.Authorize(context.Background(), tenantClaims(userOK), "GET", "/api/v1/things")
		if !errors.Is(err, boom) || d.Verdict == auth.VerdictAllowed {
			t.Errorf("got %+v, %v", d, err)
		}
	})
	t.Run("grant lookup", func(t *testing.T) {
		s, _, acc := newFixture()
		acc.grantErr = boom
		d, err := s.Authorize(context.Background(), tenantClaims(userOK), "GET", "/api/v1/things")
		if !errors.Is(err, boom) || d.Verdict == auth.VerdictAllowed {
			t.Errorf("got %+v, %v", d, err)
		}
	})
}

func TestAuthorizeUsesTheCache(t *testing.T) {
	s, eps, acc := newFixture()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if d, _ := s.Authorize(ctx, tenantClaims(userOK), "GET", "/api/v1/things"); d.Verdict != auth.VerdictAllowed {
			t.Fatalf("round %d: %+v", i, d)
		}
	}
	if eps.calls.Load() != 1 || acc.userCalls.Load() != 1 || acc.grantsCalls.Load() != 1 {
		t.Errorf("database calls: endpoints=%d users=%d grants=%d, want 1 each",
			eps.calls.Load(), acc.userCalls.Load(), acc.grantsCalls.Load())
	}
}

func TestCacheKeysIncludeTheTenant(t *testing.T) {
	// The same user id in another tenant must not reuse the first tenant's cached answer.
	s, _, acc := newFixture()
	ctx := context.Background()
	if d, _ := s.Authorize(ctx, tenantClaims(userOK), "GET", "/api/v1/things"); d.Verdict != auth.VerdictAllowed {
		t.Fatalf("tenant A: %+v", d)
	}
	acc.users = map[string]UserAccess{} // in tenant B this user id does not exist
	other := &auth.Claims{UserID: userOK, TenantID: tenantB, Level: auth.LevelTenant}
	d, err := s.Authorize(ctx, other, "GET", "/api/v1/things")
	if err != nil || d.Verdict != auth.VerdictUnauthenticated {
		t.Errorf("tenant B got %+v, %v; the cache must be per tenant", d, err)
	}
}

func TestInvalidateMakesChangesVisibleAtOnce(t *testing.T) {
	s, _, acc := newFixture()
	ctx := context.Background()
	check := func() auth.Verdict {
		d, err := s.Authorize(ctx, tenantClaims(userOK), "GET", "/api/v1/things")
		if err != nil {
			t.Fatal(err)
		}
		return d.Verdict
	}
	if check() != auth.VerdictAllowed {
		t.Fatal("setup")
	}

	acc.grants[group1] = nil // revoked in the database
	if check() != auth.VerdictAllowed {
		t.Error("a change is only seen after the TTL or an invalidation (this is the documented trade-off)")
	}
	s.InvalidateGroup(tenantA, group1)
	if check() != auth.VerdictForbidden {
		t.Error("InvalidateGroup must make a revoked grant effective at once")
	}

	acc.users[userOK] = UserAccess{GroupID: str(group1), Active: false}
	s.InvalidateUser(tenantA, userOK)
	if check() != auth.VerdictUnauthenticated {
		t.Error("InvalidateUser must make a deactivation effective at once")
	}
}

func TestInvalidateEndpoints(t *testing.T) {
	s, eps, _ := newFixture()
	ctx := context.Background()
	if d, _ := s.Authorize(ctx, tenantClaims(userOK), "GET", "/api/v1/new"); d.Reason != ReasonUnregistered {
		t.Fatalf("setup: %+v", d)
	}
	eps.list = append(append([]endpoint.Endpoint{}, catalog...), endpoint.Endpoint{ID: "e-new", Method: "GET", Path: "/api/v1/new", Scope: endpoint.ScopeTenant})
	s.InvalidateEndpoints()
	if d, _ := s.Authorize(ctx, tenantClaims(userOK), "GET", "/api/v1/new"); d.Reason != ReasonNotGranted {
		t.Errorf("after the sync the route is known but not granted: %+v", d)
	}
}

func TestAssignable(t *testing.T) {
	s, _, _ := newFixture()
	got, err := s.Assignable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	want := []string{"e-list", "e-del", "e-get"} // sorted by path, then method (DELETE before GET); no public, no platform
	if len(ids) != len(want) {
		t.Fatalf("Assignable = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("Assignable = %v, want %v", ids, want)
		}
	}
}
