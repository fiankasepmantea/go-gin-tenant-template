package tenant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContext(t *testing.T) {
	if _, ok := IDFrom(context.Background()); ok {
		t.Fatal("an empty context must not have a tenant")
	}
	ctx := WithID(context.Background(), "t-123")
	if id, ok := IDFrom(ctx); !ok || id != "t-123" {
		t.Fatalf("IDFrom = %q, %v", id, ok)
	}
	if _, ok := IDFrom(WithID(context.Background(), "")); ok {
		t.Fatal("an empty ID must not count as present")
	}
}

func TestNormalizeCode(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"tokoa", "tokoa", true},
		{"  TokoA ", "tokoa", true},
		{"toko-a1", "toko-a1", true},
		{"a", "a", true},
		{"", "", false},
		{"-toko", "", false},
		{"toko-", "", false},
		{"toko a", "", false},
		{"toko_a", "", false},
		{"toko.a", "", false},
		{"../etc", "", false},
	}
	for _, tc := range tests {
		got, ok := NormalizeCode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("NormalizeCode(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func newReq(host string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "http://"+host+"/login", nil)
	r.Host = host
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestHeaderResolver(t *testing.T) {
	res := HeaderResolver{}
	if code, ok := res.Resolve(newReq("x.test", map[string]string{"X-Tenant-Code": " TokoA "})); !ok || code != "tokoa" {
		t.Errorf("= (%q, %v)", code, ok)
	}
	if _, ok := res.Resolve(newReq("x.test", nil)); ok {
		t.Error("should fail without a header")
	}

	custom := HeaderResolver{Name: "X-Company"}
	if code, ok := custom.Resolve(newReq("x.test", map[string]string{"X-Company": "pt-maju"})); !ok || code != "pt-maju" {
		t.Errorf("custom = (%q, %v)", code, ok)
	}
}

func TestSubdomainResolver(t *testing.T) {
	res := SubdomainResolver{BaseDomain: "app.com"}

	tests := []struct {
		host string
		want string
		ok   bool
	}{
		{"tokoa.app.com", "tokoa", true},
		{"TokoA.App.com:8080", "tokoa", true},
		{"tokoa.app.com.", "tokoa", true},
		{"app.com", "", false},
		{"www.app.com", "", false},
		{"api.app.com", "", false},
		{"a.b.app.com", "", false},
		{"tokoa.other.com", "", false},
		{"evilapp.com", "", false},
		{"localhost:8080", "", false},
	}
	for _, tc := range tests {
		got, ok := res.Resolve(newReq(tc.host, nil))
		if got != tc.want || ok != tc.ok {
			t.Errorf("host %q = (%q, %v), want (%q, %v)", tc.host, got, ok, tc.want, tc.ok)
		}
	}

	if _, ok := (SubdomainResolver{}).Resolve(newReq("tokoa.app.com", nil)); ok {
		t.Error("should always fail without a BaseDomain")
	}
}

func TestChain_Order(t *testing.T) {
	res := Chain(SubdomainResolver{BaseDomain: "app.com"}, HeaderResolver{})

	// subdomain wins over header
	r := newReq("tokoa.app.com", map[string]string{"X-Tenant-Code": "tokob"})
	if code, ok := res.Resolve(r); !ok || code != "tokoa" {
		t.Errorf("subdomain must win: (%q, %v)", code, ok)
	}

	// without a subdomain, falls back to the header (mobile)
	r = newReq("api.app.com", map[string]string{"X-Tenant-Code": "tokob"})
	if code, ok := res.Resolve(r); !ok || code != "tokob" {
		t.Errorf("fallback header: (%q, %v)", code, ok)
	}

	// neither is present
	if _, ok := res.Resolve(newReq("api.app.com", nil)); ok {
		t.Error("should fail without any hint")
	}
}
