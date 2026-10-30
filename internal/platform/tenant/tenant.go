// Package tenant holds the tenant context and how to identify the tenant from a request.
//
// Apartment analogy: the tenant code is the unit number. At login, the clerk (resolver)
// determines which unit is meant. After entering, the unit number is stored in the token
// and only that token is trusted; the header or subdomain is no longer used.
package tenant

import (
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// DefaultHeader is the default header name for mobile/API clients.
const DefaultHeader = "X-Tenant-Code"

// ----- context -----

type ctxKey struct{}

// WithID stores the tenant_id in the context.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// IDFrom retrieves the tenant_id from the context.
func IDFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(ctxKey{}).(string)
	return id, ok && id != ""
}

// ----- tenant code -----

var codePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeCode normalizes (lowercase, trimmed) and validates a tenant code.
func NormalizeCode(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if !codePattern.MatchString(s) {
		return "", false
	}
	return s, true
}

// ----- resolver -----

// Resolver determines the tenant code from a request.
type Resolver interface {
	Resolve(r *http.Request) (code string, ok bool)
}

// HeaderResolver reads the tenant code from a header (suited to mobile/API).
type HeaderResolver struct {
	Name string // empty = DefaultHeader
}

// Resolve implements Resolver.
func (h HeaderResolver) Resolve(r *http.Request) (string, bool) {
	name := h.Name
	if name == "" {
		name = DefaultHeader
	}
	return NormalizeCode(r.Header.Get(name))
}

// DefaultReserved lists the subdomains that must not be treated as tenants.
var DefaultReserved = []string{"www", "api", "admin", "platform"}

// SubdomainResolver reads the tenant code from the subdomain (suited to web).
// With BaseDomain "app.com", host "tokoa.app.com" yields "tokoa".
type SubdomainResolver struct {
	BaseDomain string
	Reserved   []string // nil = DefaultReserved
}

// Resolve implements Resolver.
func (s SubdomainResolver) Resolve(r *http.Request) (string, bool) {
	base := strings.ToLower(strings.Trim(s.BaseDomain, "."))
	if base == "" {
		return "", false
	}

	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))

	suffix := "." + base
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	label := strings.TrimSuffix(host, suffix)
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}

	reserved := s.Reserved
	if reserved == nil {
		reserved = DefaultReserved
	}
	for _, name := range reserved {
		if label == name {
			return "", false
		}
	}
	return NormalizeCode(label)
}

type chain []Resolver

// Chain tries the resolvers in order and uses the first successful result.
// Recommended order: subdomain (web), then header (mobile/API).
// The last fallback, the tenant_code field in the login body, is handled by the login handler.
func Chain(resolvers ...Resolver) Resolver { return chain(resolvers) }

// Resolve implements Resolver.
func (c chain) Resolve(r *http.Request) (string, bool) {
	for _, res := range c {
		if code, ok := res.Resolve(r); ok {
			return code, true
		}
	}
	return "", false
}
