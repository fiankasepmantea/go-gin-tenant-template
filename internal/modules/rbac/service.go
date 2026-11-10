package rbac

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/core"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/modules/endpoint"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/auth"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ttlcache"
)

// Reasons of a decision. They go to the server log only.
const (
	ReasonNoClaims     = "no_claims"
	ReasonUnregistered = "unregistered_endpoint"
	ReasonWrongLevel   = "wrong_level"
	ReasonUserGone     = "user_not_found"
	ReasonUserInactive = "user_inactive"
	ReasonNoGroup      = "no_group"
	ReasonNotGranted   = "not_granted"
	ReasonAllowed      = "allowed"
	ReasonPublic       = "public"
	ReasonPlatform     = "platform"
)

// EndpointLister returns every endpoint (endpoint.Repository).
type EndpointLister interface {
	All(ctx context.Context) ([]endpoint.Endpoint, error)
}

// AccessSource reads users and grants (Repository).
type AccessSource interface {
	UserAccess(ctx context.Context, userID string) (UserAccess, error)
	GroupEndpointIDs(ctx context.Context, groupID string) ([]string, error)
}

// Options are the cache lifetimes. Zero values mean the defaults.
//
// A change made in another process (another replica, the CLI) is seen after at most one TTL. A change made
// through this process's own API is visible at once, because the service clears the affected entries.
type Options struct {
	EndpointTTL time.Duration // default 60s
	AccessTTL   time.Duration // default 30s: users and grants
	MaxEntries  int           // per cache, default 10000
}

type userKey struct{ tenant, user string }
type groupKey struct{ tenant, group string }

// Service answers "may this caller use this endpoint?". Safe for concurrent use.
type Service struct {
	endpoints EndpointLister
	access    AccessSource

	epCache    *ttlcache.Cache[struct{}, map[string]endpoint.Endpoint]
	userCache  *ttlcache.Cache[userKey, UserAccess]
	grantCache *ttlcache.Cache[groupKey, map[string]struct{}]
}

func NewService(endpoints EndpointLister, access AccessSource, opts Options) *Service {
	if opts.EndpointTTL <= 0 {
		opts.EndpointTTL = 60 * time.Second
	}
	if opts.AccessTTL <= 0 {
		opts.AccessTTL = 30 * time.Second
	}
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = 10_000
	}
	return &Service{
		endpoints:  endpoints,
		access:     access,
		epCache:    ttlcache.New[struct{}, map[string]endpoint.Endpoint](opts.EndpointTTL, 1),
		userCache:  ttlcache.New[userKey, UserAccess](opts.AccessTTL, opts.MaxEntries),
		grantCache: ttlcache.New[groupKey, map[string]struct{}](opts.AccessTTL, opts.MaxEntries),
	}
}

func (s *Service) endpointIndex(ctx context.Context) (map[string]endpoint.Endpoint, error) {
	if idx, ok := s.epCache.Get(struct{}{}); ok {
		return idx, nil
	}
	list, err := s.endpoints.All(ctx)
	if err != nil {
		return nil, err
	}
	idx := make(map[string]endpoint.Endpoint, len(list))
	for _, e := range list {
		idx[e.Key()] = e
	}
	s.epCache.Set(struct{}{}, idx)
	return idx, nil
}

func (s *Service) userAccess(ctx context.Context, tenantID, userID string) (UserAccess, error) {
	k := userKey{tenantID, userID}
	if a, ok := s.userCache.Get(k); ok {
		return a, nil
	}
	a, err := s.access.UserAccess(ctx, userID)
	if err != nil {
		return UserAccess{}, err // not cached: a missing user is re-checked on the next request
	}
	s.userCache.Set(k, a)
	return a, nil
}

func (s *Service) grants(ctx context.Context, tenantID, groupID string) (map[string]struct{}, error) {
	k := groupKey{tenantID, groupID}
	if g, ok := s.grantCache.Get(k); ok {
		return g, nil
	}
	ids, err := s.access.GroupEndpointIDs(ctx, groupID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	s.grantCache.Set(k, set)
	return set, nil
}

// Authorize decides whether the caller described by claims may call method on the Gin route pattern route.
// A non-nil error means the decision could not be made (database failure); the caller must refuse the request
// (fail closed), never allow it.
func (s *Service) Authorize(ctx context.Context, claims *auth.Claims, method, route string) (auth.Decision, error) {
	deny := func(v auth.Verdict, reason string) (auth.Decision, error) {
		return auth.Decision{Verdict: v, Reason: reason}, nil
	}
	if claims == nil {
		return deny(auth.VerdictUnauthenticated, ReasonNoClaims)
	}

	idx, err := s.endpointIndex(ctx)
	if err != nil {
		return auth.Decision{}, fmt.Errorf("rbac: load endpoints: %w", err)
	}
	ep, ok := idx[endpoint.Key(method, route)]
	if !ok {
		return deny(auth.VerdictForbidden, ReasonUnregistered) // default deny
	}

	switch claims.Level {
	case auth.LevelPlatform:
		if ep.Scope != endpoint.ScopePlatform {
			return deny(auth.VerdictForbidden, ReasonWrongLevel)
		}
		return deny(auth.VerdictAllowed, ReasonPlatform)

	case auth.LevelTenant:
		if ep.Scope != endpoint.ScopeTenant {
			return deny(auth.VerdictForbidden, ReasonWrongLevel)
		}
		acc, err := s.userAccess(ctx, claims.TenantID, claims.UserID)
		if errors.Is(err, core.ErrNotFound) {
			return deny(auth.VerdictUnauthenticated, ReasonUserGone)
		}
		if err != nil {
			return auth.Decision{}, fmt.Errorf("rbac: load user: %w", err)
		}
		if !acc.Active {
			return deny(auth.VerdictUnauthenticated, ReasonUserInactive)
		}
		if ep.IsPublic {
			return deny(auth.VerdictAllowed, ReasonPublic)
		}
		if acc.GroupID == nil {
			return deny(auth.VerdictForbidden, ReasonNoGroup)
		}
		granted, err := s.grants(ctx, claims.TenantID, *acc.GroupID)
		if err != nil {
			return auth.Decision{}, fmt.Errorf("rbac: load grants: %w", err)
		}
		if _, ok := granted[ep.ID]; !ok {
			return deny(auth.VerdictForbidden, ReasonNotGranted)
		}
		return deny(auth.VerdictAllowed, ReasonAllowed)
	}
	return deny(auth.VerdictForbidden, ReasonWrongLevel)
}

// Assignable returns the endpoints a tenant admin may grant to a group: tenant scope and not public
// (a public endpoint needs no grant, a platform endpoint must never be grantable by a tenant).
func (s *Service) Assignable(ctx context.Context) ([]endpoint.Endpoint, error) {
	idx, err := s.endpointIndex(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]endpoint.Endpoint, 0, len(idx))
	for _, e := range idx {
		if e.Scope == endpoint.ScopeTenant && !e.IsPublic {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out, nil
}

// GroupEndpointIDs returns the endpoints granted to a group (uncached: it is the source of truth shown to an admin).
func (s *Service) GroupEndpointIDs(ctx context.Context, groupID string) ([]string, error) {
	return s.access.GroupEndpointIDs(ctx, groupID)
}

// InvalidateGroup makes a change of a group's grants visible at once in this process.
func (s *Service) InvalidateGroup(tenantID, groupID string) {
	s.grantCache.Delete(groupKey{tenantID, groupID})
}

// InvalidateUser makes a change of a user's group or active flag visible at once in this process.
func (s *Service) InvalidateUser(tenantID, userID string) {
	s.userCache.Delete(userKey{tenantID, userID})
}

// InvalidateEndpoints forgets the cached endpoint list (after a synchronization in this process).
func (s *Service) InvalidateEndpoints() { s.epCache.Clear() }
